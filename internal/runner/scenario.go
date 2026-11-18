// Package runner drives the end-to-end two-connection relay scenario.
package runner

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"

	"relaydiag/internal/config"
	"relaydiag/internal/relay"
	"relaydiag/internal/stats"
	"relaydiag/internal/transport"
)

const (
	clockRate   = 90000 // RTP clock rate used for timestamps and jitter
	payloadSize = 160   // bytes of deterministic payload per packet
	ssrc        = 13277
)

// Summary is the stable per-run result. Field names are part of the CLI
// contract and must not change.
type Summary struct {
	CandidatePairs     int     `json:"candidate_pairs"`
	SelectedPairState  string  `json:"selected_pair_state"`
	ForwardedPackets   uint64  `json:"forwarded_packets"`
	LostPackets        uint64  `json:"lost_packets"`
	DuplicatePackets   uint64  `json:"duplicate_packets"`
	ReorderedPackets   uint64  `json:"reordered_packets"`
	JitterMs           float64 `json:"jitter_ms"`
	JitterInsufficient bool    `json:"jitter_samples_insufficient"`
	DurationMs         int64   `json:"duration_ms"`
}

// Run executes the scenario: sender -> connection A -> relay forwarder ->
// connection B -> receiver, all on loopback. It returns the statistics
// summary or a diagnostic error.
func Run(ctx context.Context, cfg config.Config) (Summary, error) {
	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()

	names := []string{"sender", "relay-in", "relay-out", "receiver"}
	peers := make([]*transport.Peer, 0, len(names))
	for _, n := range names {
		p, err := transport.NewPeer(n)
		if err != nil {
			return Summary{}, err
		}
		peers = append(peers, p)
	}
	sender, relayIn, relayOut, receiver := peers[0], peers[1], peers[2], peers[3]

	var wg sync.WaitGroup
	defer func() {
		// Closing the peer connections ends the tracks, which lets the
		// forwarding and receive goroutines exit before we return.
		for _, p := range peers {
			_ = p.Close()
		}
		wg.Wait()
	}()

	capability := webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeVP8}
	sendTrack, err := webrtc.NewTrackLocalStaticRTP(capability, "relaydiag-a", "relaydiag")
	if err != nil {
		return Summary{}, fmt.Errorf("runner: create send track: %w", err)
	}
	relayTrack, err := webrtc.NewTrackLocalStaticRTP(capability, "relaydiag-b", "relaydiag")
	if err != nil {
		return Summary{}, fmt.Errorf("runner: create relay track: %w", err)
	}
	if _, err := sender.PC.AddTrack(sendTrack); err != nil {
		return Summary{}, fmt.Errorf("runner: add send track: %w", err)
	}
	if _, err := relayOut.PC.AddTrack(relayTrack); err != nil {
		return Summary{}, fmt.Errorf("runner: add relay track: %w", err)
	}

	if err := dial(ctx, sender, relayIn, "A"); err != nil {
		return Summary{}, err
	}
	if err := dial(ctx, relayOut, receiver, "B"); err != nil {
		return Summary{}, err
	}
	pairs := transport.CollectPairs(relayIn.PC)

	// The remote tracks exist as soon as the remote descriptions are
	// applied; waiting for OnTrack would deadlock because it only fires
	// once media flows, and we have not sent anything yet.
	inTrack, err := firstRemoteTrack(relayIn.PC)
	if err != nil {
		return Summary{}, fmt.Errorf("runner: connection A: %w", err)
	}
	outTrack, err := firstRemoteTrack(receiver.PC)
	if err != nil {
		return Summary{}, fmt.Errorf("runner: connection B: %w", err)
	}

	fwd := relay.NewForwarder(relayTrack)
	checker := relay.NewChecker()
	window := stats.NewWindow(clockRate)
	errCh := make(chan error, 1)
	allReceived := make(chan struct{})

	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := fwd.Run(ctx, inTrack); err != nil {
			select {
			case errCh <- err:
			default:
			}
		}
	}()
	go func() {
		defer wg.Done()
		for {
			pkt, _, err := outTrack.ReadRTP()
			if err != nil {
				return // track closed during shutdown
			}
			checker.Check(pkt.SequenceNumber)
			window.Add(pkt.Timestamp, time.Now())
			if checker.Snapshot().Received >= uint64(cfg.Packets) {
				close(allReceived)
				return
			}
		}
	}()

	if err := sendAll(ctx, sendTrack, cfg); err != nil {
		return Summary{}, err
	}

	select {
	case <-allReceived:
	case err := <-errCh:
		return Summary{}, err
	case <-ctx.Done():
		c := checker.Snapshot()
		return Summary{}, fmt.Errorf("runner: timed out waiting for packets: received %d/%d: %w",
			c.Received, cfg.Packets, ctx.Err())
	}

	if n := fwd.Corrupted(); n > 0 {
		return Summary{}, fmt.Errorf("runner: %d forwarded packets failed content verification", n)
	}

	counts := checker.Snapshot()
	snap := window.Snapshot()
	return Summary{
		CandidatePairs:     pairs.Total,
		SelectedPairState:  pairs.SelectedState,
		ForwardedPackets:   fwd.Forwarded(),
		LostPackets:        checker.Lost(uint64(cfg.Packets)),
		DuplicatePackets:   counts.Duplicate,
		ReorderedPackets:   counts.Reordered,
		JitterMs:           snap.JitterMs,
		JitterInsufficient: snap.InsufficientSamples,
		DurationMs:         time.Since(start).Milliseconds(),
	}, nil
}

// sendAll transmits cfg.Packets deterministic RTP packets at cfg.Interval.
func sendAll(ctx context.Context, track *webrtc.TrackLocalStaticRTP, cfg config.Config) error {
	tsStep := uint32(cfg.Interval.Seconds() * clockRate)
	if tsStep == 0 {
		tsStep = 1
	}
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()
	for i := 0; i < cfg.Packets; i++ {
		seq := uint16(i) //nolint:gosec // intentional uint16 wrap per RFC 3550
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SequenceNumber: seq,
				Timestamp:      uint32(i) * tsStep, //nolint:gosec // timestamps wrap by design
				SSRC:           ssrc,
			},
			Payload: relay.ExpectedPayload(seq, payloadSize),
		}
		if err := track.WriteRTP(pkt); err != nil {
			return fmt.Errorf("runner: write packet %d: %w", i, err)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("runner: sending packet %d: %w", i, ctx.Err())
		case <-ticker.C:
		}
	}
	return nil
}

func firstRemoteTrack(pc *webrtc.PeerConnection) (*webrtc.TrackRemote, error) {
	for _, r := range pc.GetReceivers() {
		if t := r.Track(); t != nil {
			return t, nil
		}
	}
	return nil, fmt.Errorf("no remote track negotiated")
}
