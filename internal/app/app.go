// Package app wires media preparation, the job scheduler and reporting into
// the run/resume/demo/report pipeline.
package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"rateqa/internal/cli"
	"rateqa/internal/jobs"
	"rateqa/internal/media"
	"rateqa/internal/report"
	"rateqa/internal/store"
)

// Run executes parsed CLI options and returns the process exit code.
func Run(ctx context.Context, opts cli.Options) int {
	switch opts.Command {
	case cli.CmdDemo:
		return runDemoOrRun(ctx, opts, true)
	case cli.CmdRun:
		return runDemoOrRun(ctx, opts, false)
	case cli.CmdResume:
		return runResume(ctx, opts)
	case cli.CmdReport:
		return runReport(opts)
	default:
		fmt.Fprintf(os.Stderr, "rateqa: unknown command %q\n", opts.Command)
		return cli.ExitUsage
	}
}

// prepareShared validates ffmpeg, builds (or reuses) the workspace layout and
// returns segment specs plus store parameters.
func prepareShared(ctx context.Context, opts cli.Options, sourceY4M string,
	totalFrames int, hdr media.Header) ([]jobs.SegmentSpec, store.Parameters, error) {
	if err := media.EnsureFFmpeg(ctx); err != nil {
		return nil, store.Parameters{}, err
	}
	segDir := filepath.Join(opts.Workspace, "segments")
	refs, err := splitIfNeeded(sourceY4M, segDir, opts.FramesPerSegment)
	if err != nil {
		return nil, store.Parameters{}, err
	}
	fp, err := media.FingerprintFile(sourceY4M)
	if err != nil {
		return nil, store.Parameters{}, err
	}
	specs := make([]jobs.SegmentSpec, len(refs))
	for i, ref := range refs {
		specs[i] = jobs.SegmentSpec{Ref: ref, TargetKbps: float64(opts.TargetKbps)}
	}
	params := store.Parameters{
		SourceFingerprint: fp, Width: hdr.Width, Height: hdr.Height,
		FPSNum: hdr.FPSNum, FPSDen: hdr.FPSDen, Segments: len(refs),
		TargetKbps: float64(opts.TargetKbps),
		FramesPerSegment: opts.FramesPerSegment, TotalFrames: totalFrames,
	}
	return specs, params, nil
}

// splitIfNeeded reuses an existing segment set on resume/demo re-entry and
// otherwise streams the source into per-segment Y4M files.
func splitIfNeeded(sourceY4M, segDir string, framesPer int) ([]media.SegmentRef, error) {
	if entries, err := os.ReadDir(segDir); err == nil && len(entries) > 0 {
		refs := make([]media.SegmentRef, 0, len(entries))
		for _, e := range entries {
			p := filepath.Join(segDir, e.Name())
			hdr, n, err := media.CountFrames(p)
			if err != nil {
				return nil, err
			}
			idx := len(refs)
			refs = append(refs, media.SegmentRef{Index: idx, Path: p, Frames: n,
				Width: hdr.Width, Height: hdr.Height, FPSNum: hdr.FPSNum, FPSDen: hdr.FPSDen})
		}
		return refs, nil
	}
	f, err := os.Open(sourceY4M)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r, err := media.NewReader(f)
	if err != nil {
		return nil, err
	}
	return media.SplitToSegments(r, segDir, framesPer)
}

func runDemoOrRun(ctx context.Context, opts cli.Options, isDemo bool) int {
	if err := os.MkdirAll(opts.Workspace, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "rateqa: workspace: %v\n", err)
		return cli.ExitStorage
	}
	sourceY4M := opts.Input
	if isDemo {
		sourceY4M = filepath.Join(opts.Workspace, "demo-source.y4m")
		n, err := media.GenerateDemo(media.DemoConfig{
			Path: sourceY4M, Width: 176, Height: 144, FPSNum: 25, FPSDen: 1,
			Segments: opts.Segments, FramesPerSegment: opts.FramesPerSegment,
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "rateqa: demo generation: %v\n", err)
			return cli.ExitMedia
		}
		opts.Segments = 0 // actual count derived after splitting
		_ = n
	} else {
		abs, ws, err := media.EnsureSafePaths(opts.Input, opts.Workspace)
		if err != nil {
			fmt.Fprintf(os.Stderr, "rateqa: %v\n", err)
			return cli.ExitUsage
		}
		sourceY4M, opts.Workspace = abs, ws
	}
	hdr, totalFrames, err := media.CountFrames(sourceY4M)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rateqa: source validation: %v\n", err)
		return cli.ExitMedia
	}
	specs, params, err := prepareShared(ctx, opts, sourceY4M, totalFrames, hdr)
	if err != nil {
		return classifyMedia(err)
	}
	st, err := store.Open(opts.Workspace, params)
	if err != nil {
		return classifyStore(err)
	}
	defer st.Close()
	return driveScheduler(ctx, opts, st, specs)
}

func runResume(ctx context.Context, opts cli.Options) int {
	cpPath := filepath.Join(opts.Workspace, "checkpoint.json")
	cp, err := readCheckpoint(cpPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rateqa: cannot resume: %v\n", err)
		return cli.ExitStorage
	}
	// Resume must keep the original run parameters; the CLI target must agree.
	if float64(opts.TargetKbps) != cp.Params.TargetKbps {
		fmt.Fprintf(os.Stderr, "rateqa: %v: target-kbps %d differs from stored %.0f\n",
			store.ErrFingerprintMismatch, opts.TargetKbps, cp.Params.TargetKbps)
		return cli.ExitUsage
	}
	specs, err := specsFromWorkspace(opts.Workspace, cp.Params)
	if err != nil {
		return classifyMedia(err)
	}
	if err := media.EnsureFFmpeg(ctx); err != nil {
		return classifyMedia(err)
	}
	st, err := store.Open(opts.Workspace, cp.Params)
	if err != nil {
		return classifyStore(err)
	}
	defer st.Close()
	fmt.Fprintf(os.Stdout, "resume: recovered_candidates=%d\n", st.Recovered())
	return driveScheduler(ctx, opts, st, specs)
}

func driveScheduler(ctx context.Context, opts cli.Options, st *store.Store,
	specs []jobs.SegmentSpec) int {
	sched := jobs.New(st, opts.Workspace, jobs.Config{
		Workers: opts.Workers, FailAfter: opts.FailAfter,
	}, jobs.NewFFmpegExecutor(opts.Workspace))
	results, err := sched.Run(ctx, specs)
	if errors.Is(err, jobs.ErrInterrupted) || errors.Is(ctx.Err(), context.Canceled) {
		// Rebuild partial results from durable completed jobs so the
		// interrupted report carries real numbers instead of zeros.
		results = resultsFromStore(st, specs)
		rep := report.Build(st, results)
		rep.MarkIncomplete()
		_, _ = rep.WriteJSONLFile(opts.Workspace)
		rep.PrintTerminal(os.Stdout)
		fmt.Fprintf(os.Stderr, "rateqa: interrupted; resume with: rateqa resume --workspace %s\n",
			opts.Workspace)
		return cli.ExitInterrupted
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "rateqa: processing failed: %v\n", err)
		return classifyMedia(err)
	}
	rep := report.Build(st, results)
	path, werr := rep.WriteJSONLFile(opts.Workspace)
	if werr != nil {
		fmt.Fprintf(os.Stderr, "rateqa: report write: %v\n", werr)
		return cli.ExitStorage
	}
	rep.PrintTerminal(os.Stdout)
	fmt.Fprintf(os.Stdout, "report=%s\n", path)
	return cli.ExitOK
}

func runReport(opts cli.Options) int {
	cp, err := readCheckpoint(filepath.Join(opts.Workspace, "checkpoint.json"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "rateqa: %v\n", err)
		return cli.ExitStorage
	}
	specs, err := specsFromWorkspace(opts.Workspace, cp.Params)
	if err != nil {
		return classifyMedia(err)
	}
	st, err := store.Open(opts.Workspace, cp.Params)
	if err != nil {
		return classifyStore(err)
	}
	defer st.Close()
	results := resultsFromStore(st, specs)
	rep := report.Build(st, results)
	path, err := rep.WriteJSONLFile(opts.Workspace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rateqa: %v\n", err)
		return cli.ExitStorage
	}
	rep.PrintTerminal(os.Stdout)
	fmt.Fprintf(os.Stdout, "report=%s\n", path)
	return cli.ExitOK
}

func classifyStore(err error) int {
	fmt.Fprintf(os.Stderr, "rateqa: storage: %v\n", err)
	if errors.Is(err, store.ErrFingerprintMismatch) {
		return cli.ExitUsage
	}
	return cli.ExitStorage
}

func classifyMedia(err error) int {
	fmt.Fprintf(os.Stderr, "rateqa: media/encode: %v\n", err)
	if errors.Is(err, store.ErrFingerprintMismatch) {
		return cli.ExitUsage
	}
	return cli.ExitMedia
}
