// Package pipeline wires every subsystem together: WAV/RTP parsing feeds the
// unified time base, the analyzer localizes damage, the planner applies the
// fixed decision priority, deliverable segments run through a bounded pool,
// and the merger restores deterministic order. Cancellation propagates from
// the producer through workers to the merge side; completed work survives a
// canceled run via the resume checkpoint.
package pipeline

import (
	"context"
	"errors"
	"fmt"

	"mediad/analyze"
	"mediad/internal/fixture"
	"mediad/media"
	"mediad/merge"
	"mediad/plan"
	"mediad/pool"
	"mediad/stream"
	"mediad/wav"
)

// MaxBlockBytes bounds memory per segment: a WAV payload larger than this is
// rejected as a worker failure instead of being buffered for processing.
const MaxBlockBytes = 1 << 20 // 1 MiB

// Config controls one pipeline run.
type Config struct {
	Workers  int
	Queue    int
	BudgetMs uint64
	MaxBytes int64
}

// Outcome is the full structured result handed to the report adapter.
type Outcome struct {
	Analysis *analyze.Report
	Plan     *plan.Plan
	Merge    *merge.Order
	Canceled bool
}

// checkpoint is the resume state: sequences already processed successfully.
type checkpoint map[int]bool

// Run executes the full pipeline against self-generated fixture cases. When
// ctx is canceled it returns the partial Outcome together with ctx.Err() so
// callers can distinguish cancellation from an internal failure.
func Run(ctx context.Context, cases []fixture.Case, cfg Config) (*Outcome, error) {
	return runWithCheckpoint(ctx, cases, cfg, checkpoint{})
}

// Resume repeats a run while skipping every sequence recorded as complete in
// a prior Outcome. The result is identical to a fresh run when nothing has
// been canceled, which keeps reruns reviewable.
func Resume(ctx context.Context, cases []fixture.Case, cfg Config, prev *Outcome) (*Outcome, error) {
	cp := checkpoint{}
	if prev != nil && prev.Merge != nil {
		for _, it := range prev.Merge.Items {
			cp[it.Sequence] = true
		}
	}
	return runWithCheckpoint(ctx, cases, cfg, cp)
}

// parseCase converts one fixture case into an analyzer segment on the
// unified sample axis, accumulating faults from both WAV and RTP layers.
func parseCase(tb media.TimeBase, idx int, c fixture.Case) analyze.Segment {
	f := wav.ParseChunks(c.WAV)
	pcm := wav.ValidatePCM(f, f.Faults)
	faults := append([]media.Fault(nil), pcm.Faults...)

	if len(c.RTP) > 0 {
		dec := stream.NewDecoder(tb)
		rtpRes := dec.Decode(c.RTP)
		faults = append(faults, rtpRes.Faults...)
	}

	end := c.SampleStart
	if pcm.FrameCount > 0 {
		end = c.SampleStart + pcm.FrameCount
	}
	return analyze.Segment{
		InputIndex:  idx,
		Name:        c.Name,
		SampleStart: c.SampleStart,
		SampleEnd:   end,
		Bytes:       int64(len(c.WAV)),
		Faults:      faults,
	}
}

func runWithCheckpoint(ctx context.Context, cases []fixture.Case, cfg Config, cp checkpoint) (*Outcome, error) {
	tb := media.TimeBase{SampleRate: fixture.SampleRate, RTPClock: fixture.SampleRate}

	segments := make([]analyze.Segment, len(cases))
	for i, c := range cases {
		segments[i] = parseCase(tb, i, c)
	}
	analysis := analyze.Run(segments)
	decision, err := plan.Decide(analysis.Intervals, tb, plan.Constraints{
		BudgetMs: cfg.BudgetMs, MaxBytes: cfg.MaxBytes,
	})
	if err != nil {
		return nil, fmt.Errorf("planning failed: %w", err)
	}

	coords := make(map[int][2]int64)
	merger := merge.NewMerger(coords)
	p := pool.New(ctx, cfg.Workers, cfg.Queue, func(ctx context.Context, t pool.Task) (any, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		seg := segments[t.Sequence]
		if seg.Bytes > MaxBlockBytes {
			return nil, fmt.Errorf("%w: %d > %d", errBlockTooLarge, seg.Bytes, MaxBlockBytes)
		}
		// Build the MP4 structural artifact for the deliverable segment so
		// go-mp4's sample description/keyframe path is exercised per item.
		frames := seg.SampleEnd - seg.SampleStart
		keys := make([]uint32, 0, 1)
		if frames > 0 {
			keys = append(keys, 1)
		}
		art, err := media.BuildMP4Artifact(
			media.SampleDescription{
				Channels: fixture.Channels, BitsPer: fixture.BitsPer,
				SampleRate: fixture.SampleRate,
			},
			media.KeyframeIndex{SampleNumbers: keys},
		)
		if err != nil {
			return nil, err
		}
		return art, nil
	})

	canceled := false
	for _, item := range decision.Items {
		if item.Decision != plan.DecisionDeliver {
			continue // rejected/skipped items never reach workers
		}
		if cp[item.InputIndex] {
			merger.Ingest(pool.Result{Sequence: item.InputIndex, Value: "resumed"})
			continue
		}
		coords[item.InputIndex] = [2]int64{item.SampleStart, item.SampleEnd}
		task := pool.Task{Sequence: item.InputIndex, Payload: item.Name}
		if err := p.Submit(ctx, task); err != nil {
			canceled = errors.Is(err, context.Canceled)
			break
		}
	}

	// Drain whatever completed. Under cancellation the pool closes its
	// result channel on Shutdown, so the range loop always terminates.
	_ = p.Shutdown()
	for r := range p.Results() {
		merger.Ingest(r)
	}
	order := merger.Collect()

	out := &Outcome{Analysis: analysis, Plan: decision, Merge: order, Canceled: canceled}
	if canceled {
		return out, context.Canceled
	}
	return out, nil
}

// errBlockTooLarge marks the memory-bound rejection of an oversized block.
var errBlockTooLarge = errors.New("block exceeds size cap")
