package receive

import (
	"errors"
	"time"

	"jbbench/internal/jitter"
	"jbbench/internal/metrics"
	"jbbench/internal/protocol"
	"jbbench/internal/sim"
)

// Replay drives the offline path: events are processed in arrival order and
// the logical receive clock advances per event. The same jitter.Player and
// concealment logic as the live UDP path are used. totalFrames is the
// configured sender frame count; impairment fixtures outside that range do
// not contribute to playback accounting.
func Replay(events []sim.Event, totalFrames int,
	cfg jitter.PlayerConfig) (metrics.Report, error) {
	collector := metrics.New(uint64(totalFrames))
	player := jitter.NewPlayer(cfg)
	var lastSeq uint16
	var hasSeq, anchored bool
	var now time.Duration

	for _, e := range events {
		if e.ArriveNS > now {
			now = e.ArriveNS
		}
		if e.Kind == sim.EventLost {
			continue // missing frames surface during Drain
		}

		pkt, err := protocol.Decode(e.Datagram)
		if err != nil {
			classifyError(err, collector.Drops())
			continue
		}
		if e.Kind == sim.EventAmbiguous {
			// The simulator's ambiguity fixture must be rejected even if its
			// modular distance is not ambiguous relative to the last accepted
			// packet; count it independently without touching playback.
			collector.Drops().Ambiguous++
			continue
		}
		if applySeqGate(&pkt, lastSeq, hasSeq, collector.Drops()) {
			continue
		}

		if reason := player.Push(pkt, e.ArriveNS); reason != "" {
			accountWindowDrop(reason, collector.Drops())
			continue
		}
		if !anchored {
			player.SetClockAnchor(e.ArriveNS, pkt.SendNS)
			anchored = true
		}
		collector.AddReceived()
		lastSeq, hasSeq = pkt.Seq, true

		for _, o := range player.Drain(e.ArriveNS) {
			recordOutcome(collector, player, o)
		}
	}

	flushed, gaps := player.FlushRemaining(now)
	for _, o := range flushed {
		recordOutcome(collector, player, o)
	}
	if gaps > 0 {
		collector.AddPendingGaps(gaps)
	}

	st := player.Window().Stats()
	d := collector.Drops()
	d.Reordered = st.Reordered
	cs := player.Concealer().Stats()
	collector.SetConcealment(cs.MaxConcealRun, cs.MaxRecoveryJump,
		cs.RecoveryEpisodes, cs.SmoothRecoveries)
	collector.SetFrames(uint64(totalFrames))
	return collector.Build(), nil
}

func classifyError(err error, d *metrics.DropCounters) {
	switch {
	case errors.Is(err, protocol.ErrChecksum):
		d.Corrupt++
	case errors.Is(err, protocol.ErrUnknownVersion):
		d.BadVersion++
	case errors.Is(err, protocol.ErrIllegalLength), errors.Is(err, protocol.ErrShort):
		d.BadLength++
	default:
		d.Corrupt++
	}
}

func accountWindowDrop(reason string, d *metrics.DropCounters) {
	switch reason {
	case "duplicate":
		d.Duplicate++
	case "late":
		d.Late++
	case "overflow":
		d.Overflow++
	case "ambiguous":
		d.Ambiguous++
	}
}
