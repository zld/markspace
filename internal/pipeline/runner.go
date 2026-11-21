package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"bitrack/internal/analysis"
	"bitrack/internal/checkpoint"
	"bitrack/internal/cli"
	"bitrack/internal/media"
	"bitrack/internal/model"
	"bitrack/internal/rate"
	"bitrack/internal/report"
	"bitrack/internal/scheduler"
	"bitrack/internal/task"
)

// ErrInterrupted is returned when fail-after injection parks the run so the
// CLI can map it to exit code 75.
var ErrInterrupted = fmt.Errorf("run interrupted by failure injection")

// Runner owns the end-to-end flow for one video and one target bitrate.
type Runner struct {
	opts     *cli.Options
	store    *checkpoint.Store
	jobID    string
	injector Injector
}

// NewRunner constructs a runner. jobID distinguishes manifest jobs; single
// video runs use "default".
func NewRunner(opts *cli.Options, jobID string, injector Injector) *Runner {
	if injector == nil {
		injector = NoopInjector{}
	}
	return &Runner{opts: opts, jobID: jobID, injector: injector}
}

// Run executes a fresh pipeline (no prior checkpoint).
func (r *Runner) Run(ctx context.Context) (*model.Checkpoint, error) {
	store, err := checkpoint.NewStore(r.opts.WorkDir)
	if err != nil {
		return nil, err
	}
	r.store = store
	if err := os.MkdirAll(filepath.Join(r.opts.WorkDir, "segments"), 0o755); err != nil {
		return nil, fmt.Errorf("create segments dir: %w", err)
	}
	inputPath := r.opts.ResolveInput()
	if err := media.EnsureFileExists(inputPath); err != nil {
		return nil, err
	}
	probe, err := media.Probe(ctx, inputPath)
	if err != nil {
		return nil, err
	}
	fingerprint, err := checkpoint.FileSHA256(inputPath)
	if err != nil {
		return nil, err
	}
	params := r.buildParams(probe)
	digest, err := checkpoint.DigestParams(params)
	if err != nil {
		return nil, err
	}
	baselineDigest, err := checkpoint.DigestBaseline(params)
	if err != nil {
		return nil, err
	}
	params.BaselineDigest = baselineDigest

	ranges, err := analysis.BuildRanges(probe.NbFrames, int(probe.FrameRate*r.opts.SegmentSeconds+0.5))
	if err != nil {
		return nil, err
	}
	scores, err := analysis.AnalyzeComplexity(ctx, inputPath, probe, ranges, 2)
	if err != nil {
		return nil, err
	}
	frameCounts := make([]int, len(ranges))
	mins := make([]int64, len(ranges))
	totalBits := int64(float64(params.TargetKbps)*1000.0*probe.DurationSec + 0.5)
	for i, rg := range ranges {
		frameCounts[i] = rg.FrameCount
		dur := float64(rg.FrameCount) / probe.FrameRate
		mins[i] = int64(params.MinSegmentKbps) * 1000 * int64(dur*1000+1) / 1000
		if mins[i] < 1 {
			mins[i] = 1
		}
	}
	budgets, err := rate.Allocate(totalBits, scores, frameCounts, mins)
	if err != nil {
		return nil, err
	}
	segs := make([]*model.Segment, len(ranges))
	for i, rg := range ranges {
		segs[i] = &model.Segment{
			Index: rg.Index, StartFrame: rg.StartFrame, FrameCount: rg.FrameCount,
			TargetBits: budgets[i], Complexity: scores[i], State: model.StateQueued,
		}
	}
	cp := &model.Checkpoint{
		Version: 1, JobID: r.jobID, InputPath: inputPath, InputSHA256: fingerprint,
		ParamsDigest: digest, BaselineDigest: baselineDigest,
		Probe: probe, Params: params, Segments: segs,
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}
	if err := store.Save(cp); err != nil {
		return nil, err
	}
	return r.schedule(ctx, cp)
}

// Resume reuses a checkpoint after verifying every committed segment, then
// re-queues the rest and continues. Fingerprint/parameter mismatch aborts.
func (r *Runner) Resume(ctx context.Context) (*model.Checkpoint, int, error) {
	store, err := checkpoint.NewStore(r.opts.WorkDir)
	if err != nil {
		return nil, 0, err
	}
	r.store = store
	cp, err := store.Load()
	if err != nil {
		return nil, 0, fmt.Errorf("resume requires a readable checkpoint: %w", err)
	}
	currentFingerprint, err := checkpoint.FileSHA256(cp.InputPath)
	if err != nil {
		return nil, 0, err
	}
	if currentFingerprint != cp.InputSHA256 {
		return nil, 0, fmt.Errorf("resume rejected: input file changed since checkpoint (fingerprint mismatch)")
	}
	digest, err := checkpoint.DigestParams(cp.Params)
	if err != nil {
		return nil, 0, err
	}
	if digest != cp.ParamsDigest {
		return nil, 0, fmt.Errorf("resume rejected: encoding parameters changed; rerun from scratch")
	}
	reused := 0
	for _, seg := range cp.Segments {
		switch seg.State {
		case model.StateCommitted:
			decision := checkpoint.VerifyCommitted(seg)
			if !decision.Reusable {
				// Truncated or tampered output: re-encode from scratch.
				seg.State = model.StateQueued
				seg.OutputPath, seg.Checksum, seg.Attempts = "", "", nil
				seg.LastError = decision.Reason
			} else {
				seg.Attempts = append(seg.Attempts, model.Attempt{Reason: "reused-from-checkpoint"})
				reused++
			}
		case model.StatePaused, model.StateRetryable, model.StateRunning, model.StateValidating:
			if err := task.Move(seg, model.StateQueued); err != nil {
				return nil, 0, err
			}
		case model.StateFailed:
			if err := task.Reopen(seg); err != nil {
				return nil, 0, err
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(r.opts.WorkDir, "segments"), 0o755); err != nil {
		return nil, 0, err
	}
	cp.InterruptKind = ""
	if err := store.Save(cp); err != nil {
		return nil, 0, err
	}
	out, err := r.schedule(ctx, cp)
	return out, reused, err
}

// schedule runs the worker pool and persists the checkpoint after each commit.
func (r *Runner) schedule(ctx context.Context, cp *model.Checkpoint) (*model.Checkpoint, error) {
	proc := NewProcessor(r.opts.WorkDir, cp.InputPath, cp.Params, r.store, r.injector)
	cfg := scheduler.Config{
		Workers: r.opts.Workers, MaxRetries: 1,
		FailAfter: r.opts.FailAfter, InterruptKind: r.opts.InterruptKind,
	}
	sched := scheduler.New(cfg, proc, cp.Segments)
	sched.OnCommit(func(*model.Segment) {
		cp.UpdatedAt = time.Now().UTC()
		if err := r.store.Save(cp); err != nil {
			fmt.Fprintf(os.Stderr, "warning: checkpoint save failed: %v\n", err)
		}
	})
	res := sched.Run(ctx)
	if r.opts.FailAfter > 0 && res.Interrupted {
		cp.InterruptKind = "fail-after"
	}
	cp.UpdatedAt = time.Now().UTC()
	if err := r.store.Save(cp); err != nil {
		return nil, err
	}
	if len(res.Failed) > 0 {
		return cp, fmt.Errorf("encoding failed for segments %v: %s", res.Failed, cp.Segments[res.Failed[0]].LastError)
	}
	if res.Interrupted {
		return cp, ErrInterrupted
	}
	return cp, nil
}

func (r *Runner) buildParams(probe model.ProbeInfo) model.Params {
	return model.Params{
		TargetKbps: r.opts.TargetKbps, SegmentFrames: int(probe.FrameRate*r.opts.SegmentSeconds + 0.5),
		Width: probe.Width, Height: probe.Height, FrameRate: probe.FrameRate,
		MaxIterations: r.opts.Iterations, TolerancePct: r.opts.TolerancePct,
		MinSegmentKbps: r.opts.MinSegmentKbps, Encoder: "libx264", Preset: "medium",
		PixelFormat: "yuv420p", MaxPSNRDropDB: r.opts.MaxPSNRDropDB,
		FailureInjection: r.opts.InterruptKind,
	}
}

// ApplyBaseline loads the baseline and stamps each segment's reference PSNR,
// enabling delta_db and regression counting in the final report. Matching uses
// the bitrate-independent baseline digest plus the input fingerprint.
func ApplyBaseline(cp *model.Checkpoint, baselinePath string) error {
	rec, err := report.LoadBaseline(baselinePath, cp.InputSHA256, cp.BaselineDigest)
	if err != nil {
		return err
	}
	if len(rec.Segments) != len(cp.Segments) {
		return fmt.Errorf("baseline rejected: segment count %d != current %d",
			len(rec.Segments), len(cp.Segments))
	}
	for i, seg := range cp.Segments {
		seg.BaselinePSNR = rec.Segments[i].PSNRDB
		seg.DeltaDB = seg.BaselinePSNR - seg.PSNRDB
	}
	return nil
}
