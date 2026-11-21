// bitrack is a local batch media pipeline: it splits one progressive video
// into frame-aligned segments, allocates a total bitrate budget, runs a
// bounded closed-loop H.264 encode per segment, validates PSNR against a
// baseline and resumes from atomic checkpoints after interruptions.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"bitrack/internal/cli"
	"bitrack/internal/media"
	"bitrack/internal/model"
	"bitrack/internal/pipeline"
	"bitrack/internal/rate"
	"bitrack/internal/report"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	sub, opts, err := cli.Parse(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "bitrack: %v\n", err)
		return cli.ExitUsage
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	switch sub {
	case "fixture":
		return cmdFixture(ctx, opts)
	case "baseline":
		return cmdBaseline(ctx, opts)
	case "run":
		return cmdRun(ctx, opts)
	case "resume":
		return cmdResume(ctx, opts)
	}
	return cli.ExitUsage
}

func cmdFixture(ctx context.Context, o *cli.Options) int {
	manifest, err := media.GenerateFixture(ctx, o.WorkDir, media.FixtureSpec{
		Width: o.Width, Height: o.Height, FPS: o.FPS,
		Segments: o.Segments, FramesPerSegment: o.FramesPerSegment, TailFrames: o.TailFrames,
	})
	if err != nil {
		return failEncode(err)
	}
	if o.JSON {
		emit(manifest)
	} else {
		fmt.Printf("fixture written: %s (%d frames, %dx%d @ %d fps)\n",
			manifest.OutputPath, manifest.TotalFrames, manifest.Width, manifest.Height, manifest.FPS)
	}
	return cli.ExitOK
}

func cmdBaseline(ctx context.Context, o *cli.Options) int {
	cp, code := prepareAndRun(ctx, o)
	if code != cli.ExitOK {
		return code
	}
	path, err := report.SaveBaseline(o.WorkDir, cp)
	if err != nil {
		return failEncode(err)
	}
	if o.JSON {
		emit(report.BuildSummary(cp, "baseline", 0))
	} else {
		fmt.Printf("baseline written: %s (target %d kbps, mean PSNR %.2f dB)\n",
			path, cp.Params.TargetKbps, report.BuildSummary(cp, "baseline", 0).PSNRMeanDB)
	}
	return cli.ExitOK
}

func cmdRun(ctx context.Context, o *cli.Options) int {
	if o.InterruptKind == pipeline.FaultBadBaseline && o.BaselinePath != "" {
		if err := pipeline.BaselineCorruptor(o.BaselinePath); err != nil {
			return failEncode(err)
		}
	}
	cp, code := prepareAndRun(ctx, o)
	if code != cli.ExitOK && code != cli.ExitInterrupted {
		return code
	}
	interrupted := code == cli.ExitInterrupted

	if o.BaselinePath != "" {
		if err := pipeline.ApplyBaseline(cp, o.BaselinePath); err != nil {
			fmt.Fprintf(os.Stderr, "bitrack: %v\n", err)
			return cli.ExitUsage
		}
	}
	sum := report.BuildSummary(cp, "run", 0)
	if interrupted {
		sum.InterruptKind = cp.InterruptKind
		sum.Message = "interrupted after committed segments; resume with: bitrack resume --workdir ..."
	}
	if o.JSON {
		emit(sum)
	} else {
		printHuman(sum)
	}
	switch {
	case interrupted:
		return cli.ExitInterrupted
	case sum.RegressionCount > 0:
		fmt.Fprintf(os.Stderr, "bitrack: %d segment(s) regressed beyond %.2f dB\n",
			sum.RegressionCount, o.MaxPSNRDropDB)
		return cli.ExitRegression
	}
	return cli.ExitOK
}

func cmdResume(ctx context.Context, o *cli.Options) int {
	runner := pipeline.NewRunner(o, "default", pipeline.NoopInjector{})
	cp, reused, err := runner.Resume(ctx)
	if err != nil {
		return failEncode(err)
	}
	if o.BaselinePath != "" {
		if err := pipeline.ApplyBaseline(cp, o.BaselinePath); err != nil {
			fmt.Fprintf(os.Stderr, "bitrack: %v\n", err)
			return cli.ExitUsage
		}
	}
	sum := report.BuildSummary(cp, "resume", reused)
	sum.ReusedSegments = reused
	if o.JSON {
		emit(sum)
	} else {
		printHuman(sum)
	}
	fmt.Printf("resumed: reused %d committed segment(s)\n", reused)
	if sum.RegressionCount > 0 {
		return cli.ExitRegression
	}
	return cli.ExitOK
}

// prepareAndRun handles fixture generation, injector wiring and one fresh run.
func prepareAndRun(ctx context.Context, o *cli.Options) (*model.Checkpoint, int) {
	if o.GenerateFixture || !fileExists(o.ResolveInput()) {
		if _, err := media.GenerateFixture(ctx, o.WorkDir, media.FixtureSpec{
			Width: o.Width, Height: o.Height, FPS: o.FPS,
			Segments: o.Segments, FramesPerSegment: o.FramesPerSegment, TailFrames: o.TailFrames,
		}); err != nil {
			return nil, failEncode(err)
		}
	}
	injector := buildInjector(o)
	runner := pipeline.NewRunner(o, "default", injector)
	cp, err := runner.Run(ctx)
	if err != nil {
		if errors.Is(err, pipeline.ErrInterrupted) {
			return cp, cli.ExitInterrupted
		}
		var allocErr *rate.AllocationError
		if errors.As(err, &allocErr) {
			fmt.Fprintf(os.Stderr, "bitrack: %v\n", err)
			return cp, cli.ExitUsage
		}
		return cp, failEncode(err)
	}
	return cp, cli.ExitOK
}

func buildInjector(o *cli.Options) pipeline.Injector {
	switch o.InterruptKind {
	case pipeline.FaultDiskFail:
		return pipeline.NewFaultInjector(pipeline.FaultDiskFail, 0, o.WorkDir)
	case pipeline.FaultFFmpegFail:
		return pipeline.NewFaultInjector(pipeline.FaultFFmpegFail, 0, o.WorkDir)
	default:
		return pipeline.NoopInjector{}
	}
}

func failEncode(err error) int {
	fmt.Fprintf(os.Stderr, "bitrack: %v\n", err)
	return cli.ExitEncode
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func emit(v any) {
	data, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(data))
}

func printHuman(s any) {
	data, _ := json.MarshalIndent(s, "", "  ")
	fmt.Println(string(data))
}
