// Command loudscan measures EBU R128 loudness and true peak of local audio
// files and fails builds whose assets exceed the delivery limits.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"loudscan/internal/batch"
	"loudscan/internal/cli"
	"loudscan/internal/fixture"
	"loudscan/internal/media"
	"loudscan/internal/report"
)

// Exit codes are part of the tool's contract with build scripts.
const (
	exitOK       = 0 // every file measured and within limits
	exitExceeded = 1 // at least one file over a limit or unmeasurable
	exitUsage    = 2 // bad arguments or the tool could not start
	exitFileErr  = 3 // at least one input failed to probe, decode or measure
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return exitUsage
	}
	switch args[0] {
	case "scan":
		return runScan(args[1:], stdout, stderr)
	case "fixture":
		return runFixture(args[1:], stdout, stderr)
	case "-h", "--help", "help":
		fmt.Fprintln(stdout, usage)
		return exitOK
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s\n", args[0], usage)
		return exitUsage
	}
}

const usage = `loudscan measures EBU R128 loudness and true peak of local audio.

usage:
  loudscan scan --input PATH [--input PATH ...] [options]
  loudscan fixture [--dir DIR] [--seed N]

scan options:
  --input PATH           file or directory; directories are scanned recursively
  --integrated-max LUFS  maximum integrated loudness (default -16)
  --true-peak-max dBTP   maximum true peak (default -1)
  --workers N            concurrent files, 1-8 (default min(NumCPU, 4))
  --format table|json    report format (default table)
  --layout LABELS        channel labels for non-standard layouts,
                         e.g. FL,FR,FC,LFE,BL,BR
  --max-bytes N          skip inputs larger than N bytes (default 2 GiB)
  --max-seconds S        skip inputs longer than S seconds (default 3600)
  --timeout D            per-file decode timeout (default 10m)
  --ffprobe PATH         ffprobe executable (default "ffprobe")
  --ffmpeg PATH          ffmpeg executable (default "ffmpeg")

exit codes:
  0  all files measured and within limits
  1  at least one file over a limit, or unmeasurable (no_measurement)
  2  bad arguments or the tool could not start
  3  at least one input failed to probe, decode or measure`

func runScan(args []string, stdout, stderr *os.File) int {
	cfg, err := cli.ParseScan(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "loudscan: %v\n", err)
		return exitUsage
	}

	// A missing decoder is a startup failure (exit 2), not a per-file error:
	// every input would fail identically, so fail before touching the batch.
	for _, tool := range []string{cfg.ProbePath, cfg.FFmpegPath} {
		if _, err := exec.LookPath(tool); err != nil {
			fmt.Fprintf(stderr, "loudscan: required tool %q not found: %v\n", tool, err)
			fmt.Fprintln(stderr, "install FFmpeg with: brew install ffmpeg")
			return exitUsage
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	col, err := batch.Collect(cli.SortInputs(cfg.Inputs))
	if err != nil {
		fmt.Fprintf(stderr, "loudscan: discovery failed: %v\n", err)
		return exitUsage
	}
	if col.Empty() {
		fmt.Fprintln(stderr, "loudscan: no audio files found in the given inputs")
		return exitUsage
	}

	runner := batch.New(batch.Options{
		IntegratedMax: cfg.IntegratedMax,
		TruePeakMax:   cfg.TruePeakMax,
		Workers:       cfg.Workers,
		Layout:        cfg.Layout,
		Probe:         mediaProbeOptions(cfg),
		Decode:        mediaDecodeOptions(cfg),
	})
	entries, err := runner.Run(ctx, col)
	if err != nil {
		fmt.Fprintf(stderr, "loudscan: %v\n", err)
		return exitUsage
	}

	rep := report.Build(entries)
	if err := report.Write(stdout, rep, cfg.Format); err != nil {
		fmt.Fprintf(stderr, "loudscan: cannot write report: %v\n", err)
		return exitUsage
	}
	return exitCodeFor(rep)
}

func runFixture(args []string, stdout, stderr *os.File) int {
	cfg, err := cli.ParseFixture(args, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "loudscan: %v\n", err)
		return exitUsage
	}
	set, err := fixture.Generate(cfg.Dir, cfg.Seed)
	if err != nil {
		fmt.Fprintf(stderr, "loudscan: fixture generation failed: %v\n", err)
		return exitUsage
	}
	fmt.Fprintf(stdout, "wrote %d fixtures to %s (seed %d)\n", len(set.Files), set.Dir, set.Seed)
	for _, f := range set.Files {
		fmt.Fprintf(stdout, "  %-28s %d ch  %d Hz  %s\n", filepath.Base(f.Path), f.Channels, f.Rate, f.Intent)
	}
	return exitOK
}

func exitCodeFor(rep report.Report) int {
	switch {
	case rep.Summary.Errors > 0:
		return exitFileErr
	case rep.Summary.Failed > 0 || rep.Summary.Unmeasured > 0:
		return exitExceeded
	default:
		return exitOK
	}
}

func mediaProbeOptions(cfg cli.Config) media.Options {
	return media.Options{
		FFprobePath: cfg.ProbePath,
		Limits:      cfg.Limits,
	}
}

func mediaDecodeOptions(cfg cli.Config) media.DecodeOptions {
	return media.DecodeOptions{
		FFmpegPath: cfg.FFmpegPath,
		Timeout:    cfg.Timeout,
		MaxSeconds: cfg.Limits.MaxSeconds,
	}
}
