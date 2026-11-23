// Package pipeline wires transport, codec, index, journal, timeline and
// export stages into one recoverable review-timeline build. It is the single
// reusable media-processing kernel; the CLI is a thin adapter over it.
package pipeline

import (
	"os"

	"mediaroute/internal/clock"
	"mediaroute/internal/export"
	"mediaroute/internal/fixture"
	"mediaroute/internal/journal"
	"mediaroute/internal/timeline"
	"mediaroute/internal/transport"
)

// NewRunner validates options and prepares the fixture plus journal.
func NewRunner(opts Options) (*Runner, error) {
	if opts.ReorderWindow < 1 {
		return nil, clock.E(clock.PhaseTransport, 0, -1, false,
			"reorder-window must be >= 1", nil)
	}
	if opts.LossBurst < 0 {
		return nil, clock.E(clock.PhaseTransport, 0, -1, false,
			"loss-burst must be >= 0", nil)
	}
	if opts.JournalDir == "" {
		return nil, clock.E(clock.PhaseJournal, 0, -1, false,
			"journal directory is required", nil)
	}
	cfg := DefaultFixtureConfig(opts.Seed, opts.LossBurst, opts.ReorderWindow)
	fx := fixture.Build(cfg)
	var hook journal.CrashHook
	if opts.CrashAt != "" {
		hook = crashHook(opts.CrashAt)
	}
	coord, err := journal.OpenCoordinator(opts.JournalDir, hook)
	if err != nil {
		return nil, err
	}
	return &Runner{opts: opts, fx: fx, coord: coord,
		committedFrames: map[frameKey]committedArtifact{}}, nil
}

// Run executes every stage and returns the assembled metrics.
func (r *Runner) Run() (*Metrics, error) {
	defer r.coord.Close()

	// Stage 1: transport restoration per SSRC.
	demux := transport.NewDemux(r.opts.ReorderWindow)
	reassemblers := map[uint32]*transport.Reassembler{}
	for ssrc := range r.fx.Frames {
		reassemblers[ssrc] = transport.NewReassembler(ssrc)
	}
	streamFrames, stats, err := r.runTransport(demux, reassemblers)
	if err != nil {
		return nil, err
	}
	if err := r.recordTransportStats(stats); err != nil {
		return nil, err
	}

	// Stage 2: codec inspection + keyframe index validation.
	trackInputs, keyTotal, keyHits, truncated, err := r.runCodecAndIndex(streamFrames)
	if err != nil {
		return nil, err
	}

	// Stage 3: timeline merge from committed segments only.
	tl, _, err := timeline.Merge(trackInputs)
	if err != nil {
		return nil, err
	}
	r.tl = tl
	// truncated already counts the incomplete frames the reassembler emitted;
	// Merge returns the same set as excluded evidence, so do not add it twice.
	r.metrics.TruncatedFrames = truncated
	if err := r.checkCommittedOnly(tl); err != nil {
		return nil, err
	}

	// Stage 4: deterministic export over the committed timeline.
	manifest, err := export.Build(tl.Frames)
	if err != nil {
		return nil, err
	}
	r.manifest = manifest
	matched, err := r.verifyManifestAgainstJournal(manifest)
	if err != nil {
		return nil, err
	}
	r.fillMetrics(keyTotal, keyHits)
	r.metrics.SegmentMatches = matched
	return &r.metrics, nil
}

// Manifest exposes the built manifest (nil before Run).
func (r *Runner) Manifest() *export.Manifest { return r.manifest }

// Timeline exposes the merged timeline.
func (r *Runner) Timeline() *timeline.Timeline { return r.tl }

// Cleanup removes journal artifacts (tests and repeated runs manage dirs).
func Cleanup(dir string) error { return os.RemoveAll(dir) }
