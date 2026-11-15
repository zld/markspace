package receive

import (
	"errors"
	"time"

	"jbbench/internal/jitter"
	"jbbench/internal/metrics"
	"jbbench/internal/protocol"
	"jbbench/internal/transport"
)

// LiveConfig parameterizes the UDP receive path.
type LiveConfig struct {
	Player      jitter.PlayerConfig
	ReadTimeout time.Duration // idle deadline; on expiry missing frames drain
	MaxIdle     time.Duration // stop after this much cumulative idle time
}

// DefaultLiveConfig returns sane loopback defaults.
func DefaultLiveConfig() LiveConfig {
	return LiveConfig{
		Player:      jitter.DefaultPlayerConfig(),
		ReadTimeout: 50 * time.Millisecond,
		MaxIdle:     2 * time.Second,
	}
}

// RunLive consumes datagrams from the listener until it is closed or the idle
// budget is exhausted. On return the socket has been released and a report of
// everything observed so far is produced.
func RunLive(l *transport.Listener, cfg LiveConfig) (metrics.Report, error) {
	if cfg.ReadTimeout <= 0 {
		cfg.ReadTimeout = 50 * time.Millisecond
	}
	collector := metrics.New(0)
	player := jitter.NewPlayer(cfg.Player)
	start := time.Now()
	var lastSeq uint16
	var hasSeq, anchored bool
	var idle time.Duration

	closeAndReport := func() (metrics.Report, error) {
		_ = l.Close()
		flushed, _ := player.FlushRemaining(0)
		for _, o := range flushed {
			recordOutcome(collector, player, o)
		}
		d := collector.Drops()
		d.Reordered = player.Window().Stats().Reordered
		cs := player.Concealer().Stats()
		collector.SetConcealment(cs.MaxConcealRun, cs.MaxRecoveryJump,
			cs.RecoveryEpisodes, cs.SmoothRecoveries)
		// A live stream has no predeclared frame count; the observed played
		// total (real plus concealed) is the authoritative frame universe.
		collector.SetFrames(player.PlayedCount())
		return collector.Build(), nil
	}

	for {
		data, _, err := l.ReadPacket(cfg.ReadTimeout)
		now := time.Since(start)
		if err != nil {
			if errors.Is(err, transport.ErrClosed) {
				return closeAndReport()
			}
			if errors.Is(err, transport.ErrTimeout) {
				idle += cfg.ReadTimeout
				// Timeout drives missing-frame decisions against the real clock.
				if anchored {
					for _, o := range player.Drain(now) {
						recordOutcome(collector, player, o)
					}
				}
				if cfg.MaxIdle > 0 && idle >= cfg.MaxIdle {
					return closeAndReport()
				}
				continue
			}
			if errors.Is(err, transport.ErrTruncated) {
				collector.Drops().BadLength++
				continue
			}
			_ = l.Close()
			return collector.Build(), err
		}
		idle = 0

		pkt, derr := protocol.Decode(data)
		if derr != nil {
			classifyError(derr, collector.Drops())
			continue // bad packets never change playback state
		}
		if applySeqGate(&pkt, lastSeq, hasSeq, collector.Drops()) {
			continue
		}
		if reason := player.Push(pkt, now); reason != "" {
			accountWindowDrop(reason, collector.Drops())
			continue
		}
		if !anchored {
			player.SetClockAnchor(now, pkt.SendNS)
			anchored = true
		}
		collector.AddReceived()
		lastSeq, hasSeq = pkt.Seq, true
		for _, o := range player.Drain(now) {
			recordOutcome(collector, player, o)
		}
	}
}
