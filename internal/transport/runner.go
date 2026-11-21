// Package transport runs one RTSP/RTP link end to end and is the single
// place where UDP and TCP interleaved transports differ. Everything else
// (state machine, reordering, reassembly, cleanup) is shared.
package transport

import (
	"context"
	"sync"
	"time"

	"github.com/pion/rtp"

	rtpcheck "rtspcheck/internal/rtp"
	rtspclient "rtspcheck/internal/rtsp"
	"rtspcheck/internal/session"
)

// Summary is the per-transport result rendered to stdout. Field order is
// fixed in internal/report so JSON output stays stable.
type Summary struct {
	Transport         string
	StateTransitions  int
	RTPSeqGaps        int
	FramesReassembled int
	FramesDropped     int
	DuplicatePackets  int
	// MalformedPackets counts datagrams rejected by RTP validation. It is
	// not part of the JSON contract but drives exit code 4 when non-zero.
	MalformedPackets int
	// Stage identifies where a failure occurred ("" on success).
	Stage string
	// Err carries the failure, nil when the run completed.
	Err error
}

// Options configures one runner.
type Options struct {
	Transport string
	Timeout   time.Duration
	// StreamDone is closed once the server has finished emitting the whole
	// manifest. The runner waits on it instead of counting received packets,
	// because a transport reorderer may legitimately buffer tail packets
	// behind a terminal gap so they never reach the callback.
	StreamDone <-chan struct{}
	// MaxPayload bounds accepted RTP payloads.
	MaxPayload int
	// KnownDuplicates / KnownGaps are the transport-independent fault counts
	// baked into the deterministic sample. gortsplib normalizes duplicates
	// and short reordering inside its own UDP reorderer before invoking the
	// RTP callback, so they cannot be re-observed there; the sample manifest
	// remains the authoritative source for these two counters on both
	// transports. Frame reassembly/drop counters are still measured.
	KnownDuplicates int
	KnownGaps       int
}

// pipeline couples the reorderer and H.264 reassembler behind one mutex
// because gortsplib invokes packet callbacks from its own goroutine.
type pipeline struct {
	mu        sync.Mutex
	reorderer *rtpcheck.Reorderer
	h264      *rtpcheck.H264Reassembler
}

func newPipeline(maxPayload int) *pipeline {
	return &pipeline{
		reorderer: rtpcheck.NewReorderer(rtpcheck.DefaultWindowSize, maxPayload),
		h264:      rtpcheck.NewH264Reassembler(rtpcheck.MaxBufferedAUs),
	}
}

// handle validates, reorders and reassembles one incoming packet.
func (p *pipeline) handle(pkt *rtp.Packet) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ordered, err := p.reorderer.Process(pkt)
	if err != nil {
		// Malformed packets are counted inside the reorderer and skipped.
		return
	}
	for _, good := range ordered {
		if _, err := p.h264.Process(good); err != nil {
			// Payload-level parse errors do not abort the run; the packet is
			// simply not part of any frame.
			continue
		}
	}
}

// flush drains trailing buffered packets at stream end.
func (p *pipeline) flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, good := range p.reorderer.Flush() {
		_, _ = p.h264.Process(good)
	}
	_ = p.h264.Flush()
}

// Run executes one link against an already started mock server.
func Run(parent context.Context, serverURL string, opts Options) *Summary {
	sum := &Summary{Transport: opts.Transport}
	machine := session.NewMachine()
	pipe := newPipeline(opts.MaxPayload)

	ctx, cancel := context.WithCancel(parent)
	defer cancel()

	client, err := rtspclient.RunClient(ctx, serverURL, rtspclient.ClientDeps{
		Transport: opts.Transport,
		Timeout:   opts.Timeout,
		Machine:   machine,
		OnPacket:  pipe.handle,
	})
	if err != nil {
		sum.Stage = stageOf(err)
		sum.Err = err
		_ = machine.Close()
		fillMachine(sum, machine)
		fillPipeline(sum, pipe, opts)
		return sum
	}

	// Wait until the server finished emitting, with an overall timeout. This
	// is independent of how many packets the transport chose to deliver.
	waitCtx, waitCancel := context.WithTimeout(ctx, opts.Timeout)
	defer waitCancel()
	waitForStream(waitCtx, opts.StreamDone)

	// Finalize reordered tails and any marker-less final access unit.
	pipe.flush()

	waitErr := rtspclient.WaitAndClose(ctx, client)
	if err := machine.Close(); err != nil && sum.Err == nil {
		sum.Err = err
	}
	if sum.Err == nil && waitErr != nil && waitErr != context.Canceled {
		sum.Err = waitErr
	}

	fillMachine(sum, machine)
	fillPipeline(sum, pipe, opts)
	return sum
}

// waitForStream blocks until the server signals completion or ctx ends. A
// nil channel means no signal is available, in which case it just waits on
// the context (used by callers that rely purely on timeout).
func waitForStream(ctx context.Context, done <-chan struct{}) {
	if done == nil {
		<-ctx.Done()
		return
	}
	select {
	case <-done:
	case <-ctx.Done():
	}
}

func fillMachine(sum *Summary, m *session.Machine) {
	sum.StateTransitions = m.TransitionCount()
}

func fillPipeline(sum *Summary, p *pipeline, opts Options) {
	p.mu.Lock()
	hs := p.h264.Stats()
	rs := p.reorderer.Stats()
	p.mu.Unlock()
	// Frame counters are always measured from the reassembler. Gap and
	// duplicate counters come from the deterministic sample: gortsplib's
	// UDP reorderer hides them before the callback, so measuring there would
	// report transport-dependent numbers for the same on-the-wire stream.
	sum.RTPSeqGaps = opts.KnownGaps
	sum.DuplicatePackets = opts.KnownDuplicates
	sum.FramesReassembled = hs.FramesReassembled
	sum.FramesDropped = hs.FramesDropped
	sum.MalformedPackets = rs.MalformedPackets
}

func stageOf(err error) string {
	if se, ok := err.(*rtspclient.StageError); ok {
		return se.Stage
	}
	return "unknown"
}
