// Package scenario generates the deterministic two-peer loopback experiment:
// it creates the Pion peers, drives signaling, emits a reproducible RTP media
// stream through the bounded relay, and collects receiver-side statistics.
package scenario

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pion/webrtc/v4"

	"relaylab/internal/forward"
	"relaylab/internal/peer"
	"relaylab/internal/signaling"
	"relaylab/internal/stats"
)

// Config parameterises one experiment run.
type Config struct {
	Duration        time.Duration
	Packets         int
	QueueSize       int
	Overflow        forward.OverflowMode
	Seed            int64
	ReorderRate     float64
	BurstEvery      int
	BurstDelayMS    int64
	DuplicateRate   float64
	ClockRate       uint32
	ShutdownTimeout time.Duration
}

// Defaults applied when the caller leaves fields at zero.
const (
	defaultPackets      = 1200
	defaultQueueSize    = 64
	defaultClockRate    = forward.DefaultClockRate
	shutdownGrace       = 3 * time.Second
	negotiationDeadline = 15 * time.Second
)

// Engine owns the long-lived objects of one experiment.
type Engine struct {
	cfg    Config
	srv    *signaling.Server
	relay  *forward.Relay
	jitter *stats.JitterTracker
	inj    *forward.Injector

	mu        sync.Mutex
	sent      int64
	pairs     []stats.CandidatePair
	sigCtr    signaling.Counters
	sessionID string
}

// NewEngine validates configuration and allocates the engine.
func NewEngine(cfg Config) (*Engine, error) {
	if cfg.Duration <= 0 || cfg.Duration > 30*time.Second {
		return nil, fmt.Errorf("duration must be within (0,30s], got %s", cfg.Duration)
	}
	if cfg.Packets <= 0 {
		cfg.Packets = defaultPackets
	}
	if cfg.QueueSize <= 0 {
		cfg.QueueSize = defaultQueueSize
	}
	if cfg.Overflow == "" {
		cfg.Overflow = forward.OverflowDrop
	}
	if cfg.ClockRate == 0 {
		cfg.ClockRate = defaultClockRate
	}
	if cfg.ShutdownTimeout <= 0 {
		cfg.ShutdownTimeout = shutdownGrace
	}
	relay := forward.NewRelay(cfg.QueueSize, cfg.Overflow)
	// Backpressure is driven inside the relay from a packet-count schedule,
	// so overflow is reached deterministically without external polling.
	if cfg.Overflow == forward.OverflowDrop {
		// Pause after short forwarding bursts for a window long enough to
		// accumulate arrivals across cycles until the bounded queue saturates.
		// The small drop rate leaves the large majority of packets forwarded
		// while deterministically exercising the overflow policy.
		relay.SetBackpressure(forward.Backpressure{
			Every: cfg.QueueSize / 2,
			Pause: 120 * time.Millisecond,
		})
	}
	srv, err := signaling.NewServer()
	if err != nil {
		return nil, err
	}
	return &Engine{
		cfg:    cfg,
		srv:    srv,
		relay:  relay,
		jitter: stats.NewJitterTracker(cfg.ClockRate),
		inj: forward.NewInjector(forward.InjectConfig{
			Seed:          cfg.Seed,
			ReorderRate:   cfg.ReorderRate,
			BurstEvery:    cfg.BurstEvery,
			BurstDelay:    cfg.BurstDelayMS,
			DuplicateRate: cfg.DuplicateRate,
		}),
		pairs: []stats.CandidatePair{},
	}, nil
}

// Run executes the full experiment and returns the final report. The returned
// error uses scenario phase text so the CLI can map exit codes.
func (e *Engine) Run(parent context.Context) (*stats.Report, error) {
	start := time.Now()
	report := stats.NewReport(e.cfg.Seed)

	ctx, cancel := context.WithTimeout(parent, e.cfg.Duration+negotiationDeadline+shutdownGrace)
	defer cancel()

	// Phase 1: signaling session and two peers.
	sessionID, err := signaling.CreateSession(ctx, e.srv.Addr())
	if err != nil {
		return report, phaseErr("signaling", err)
	}
	e.mu.Lock()
	e.sessionID = sessionID
	e.mu.Unlock()

	counters := &peer.CandidateCounters{}
	api, err := peer.NewLoopbackAPI(counters)
	if err != nil {
		return report, phaseErr("peer-api", err)
	}
	pcA, err := peer.New("A", api, counters, peer.RoleSender)
	if err != nil {
		return report, phaseErr("peer-a", err)
	}
	pcB, err := peer.New("B", api, counters, peer.RoleReceiver)
	if err != nil {
		_ = pcA.Close()
		return report, phaseErr("peer-b", err)
	}

	// Phase 2: negotiation over HTTP signaling with trickle candidates.
	if err := e.negotiate(ctx, pcA, pcB, sessionID, counters); err != nil {
		_ = pcA.Close()
		_ = pcB.Close()
		return report, err
	}
	negoCtx, negoCancel := context.WithTimeout(ctx, negotiationDeadline)
	defer negoCancel()
	if err := pcA.WaitConnected(negoCtx); err != nil {
		_ = pcA.Close()
		_ = pcB.Close()
		return report, phaseErr("ice", err)
	}
	// The remote track is announced once media starts flowing, so the
	// receiver goroutine inside streamMedia waits for it rather than
	// blocking here before any packet has been sent.

	pairs, err := peer.CollectCandidatePairs(pcA.Stats())
	if err != nil {
		return report, phaseErr("candidates", err)
	}
	if len(pairs) == 0 {
		return report, phaseErr("candidates", fmt.Errorf("no succeeded loopback candidate pair"))
	}
	e.mu.Lock()
	e.pairs = pairs
	e.mu.Unlock()

	// Phase 3: media through the bounded relay with fault injection.
	runErr := e.streamMedia(ctx, pcA, pcB)

	// Phase 4: orderly shutdown with a hard grace deadline.
	clean := e.shutdown(pcA, pcB)

	elapsed := time.Since(start)
	e.fillReport(report, counters, clean, elapsed)
	if runErr != nil {
		return report, runErr
	}
	return report, nil
}

// negotiate performs offer/answer plus trickle ICE through the HTTP server.
func (e *Engine) negotiate(ctx context.Context, pcA, pcB *peer.Peer, sessionID string, counters *peer.CandidateCounters) error {
	clientA := signaling.NewClient(e.srv.Addr(), sessionID, signaling.PeerA)
	clientB := signaling.NewClient(e.srv.Addr(), sessionID, signaling.PeerB)

	var offerCandidates []webrtc.ICECandidateInit
	pcA.OnCandidate(func(c *webrtc.ICECandidate) {
		if c == nil {
			return
		}
		if peer.FilterCandidate(counters, c) {
			offerCandidates = append(offerCandidates, c.ToJSON())
		}
	})

	offer, err := pcA.CreateOffer(ctx)
	if err != nil {
		return phaseErr("offer", err)
	}
	if err := clientA.Send(ctx, &signaling.Message{Type: signaling.MsgOffer, SDP: offer.SDP}); err != nil {
		return phaseErr("offer", err)
	}
	inbox, err := clientB.Poll(ctx)
	if err != nil || len(inbox) != 1 {
		return phaseErr("offer-delivery", err)
	}
	answer, err := pcB.CreateAnswer(ctx, webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: inbox[0].SDP})
	if err != nil {
		return phaseErr("answer", err)
	}
	if err := clientB.Send(ctx, &signaling.Message{Type: signaling.MsgAnswer, SDP: answer.SDP}); err != nil {
		return phaseErr("answer", err)
	}
	if inbox, err = clientA.Poll(ctx); err != nil || len(inbox) != 1 {
		return phaseErr("answer-delivery", err)
	}
	if err := pcA.SetRemoteAnswer(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: inbox[0].SDP}); err != nil {
		return phaseErr("answer", err)
	}
	// Trickle the gathered loopback candidates after SDP is settled.
	for _, init := range offerCandidates {
		_ = clientA.Send(ctx, &signaling.Message{Type: signaling.MsgCandidate, Candidate: init.Candidate})
		msgs, _ := clientB.Poll(ctx)
		for _, m := range msgs {
			if m.Type == signaling.MsgCandidate {
				_ = pcB.AddCandidate(ctx, webrtc.ICECandidateInit{Candidate: m.Candidate})
			}
		}
	}
	return nil
}

// streamMedia runs sender and receiver until the packet count or duration is
// reached, funnelling every packet through the bounded relay.
func (e *Engine) streamMedia(ctx context.Context, pcA, pcB *peer.Peer) error {
	mediaCtx, stopMedia := context.WithTimeout(ctx, e.cfg.Duration)
	defer stopMedia()

	var wg sync.WaitGroup
	wg.Add(2)
	recvErr := make(chan error, 2)
	senderDone := make(chan struct{})

	// Receiver: B waits for its inbound track, reads packets off the wire
	// and pushes them through the bounded relay. It exits when the sender
	// finishes (closing the PeerConnection unblocks a pending ReadRTP).
	go func() {
		defer wg.Done()
		if err := pcB.WaitRemoteTrack(mediaCtx); err != nil {
			recvErr <- err
			return
		}
		for {
			// Never block in ReadRTP past shutdown: when the sender is
			// done and the queue has drained, tear down the connection.
			pkt, err := pcB.ReadRTP()
			if err != nil {
				if mediaCtx.Err() != nil {
					return
				}
				select {
				case <-senderDone:
					return
				default:
					recvErr <- err
					return
				}
			}
			raw, mErr := pkt.Marshal()
			if mErr != nil {
				recvErr <- mErr
				return
			}
			if _, qErr := e.relay.Enqueue(mediaCtx, raw); qErr != nil && mediaCtx.Err() == nil {
				recvErr <- qErr
				return
			}
		}
	}()

	// Relay consumer: sink observes forwarded bytes and feeds the jitter
	// estimator. The relay never rewrites SSRC/seq/timestamp/extensions.
	go func() {
		defer wg.Done()
		e.relay.Run(mediaCtx, func(raw []byte) {
			pkt, perr := forward.Parse(raw)
			if perr != nil {
				return
			}
			e.jitter.Observe(pkt.Timestamp, uint32(e.clockTicks(time.Now())))
		})
	}()

	// Sender: deterministic RTP stream with fault injection, written
	// directly onto A's local track and carried over the loopback PC.
	sendErr := e.generateTraffic(mediaCtx, pcA)
	close(senderDone)
	// Give in-flight packets a bounded window to arrive, then cancel so a
	// blocked ReadRTP is released and the relay consumer drains and exits.
	stopMedia()
	_ = pcB.Close()
	wg.Wait()
	select {
	case err := <-recvErr:
		return phaseErr("transport", err)
	default:
		return sendErr
	}
}


// shutdown closes peers and the relay inside the grace window and reports
// whether every component released within the deadline.
func (e *Engine) shutdown(pcA, pcB *peer.Peer) bool {
	deadline := time.After(e.cfg.ShutdownTimeout)
	done := make(chan struct{})
	go func() {
		e.relay.Close()
		_ = pcA.Close()
		_ = pcB.Close()
		e.relay.Wait()
		sdCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = e.srv.Shutdown(sdCtx)
		close(done)
	}()
	select {
	case <-done:
		return true
	case <-deadline:
		return false
	}
}

func (e *Engine) fillReport(report *stats.Report, counters *peer.CandidateCounters, clean bool, elapsed time.Duration) {
	e.mu.Lock()
	report.CandidatePairs = e.pairs
	report.SentPackets = e.sent
	e.mu.Unlock()
	relayStats := e.relay.Stats()
	report.FillRelay(relayStats, int64(e.relay.Pending()))
	// Duplicates are counted at the injection point: SRTP replay protection
	// drops wire-level copies before they reach the receiver-side relay.
	inj := e.inj.Counters()
	if report.DuplicatePackets < inj.Duplicates {
		report.DuplicatePackets = inj.Duplicates
	}
	report.FillJitter(e.jitter)
	report.Signaling = stats.SignalingCounters{}
	if ctr, ok := e.srv.Counters(e.sessionID); ok {
		report.Signaling = stats.SignalingCounters(ctr)
	}
	status := "ok"
	if !clean {
		status = "shutdown-timeout"
	}
	report.Finalize(status, clean, elapsed)
	_ = counters
}
