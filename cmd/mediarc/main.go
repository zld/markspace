// Command mediarc is the terminal acceptance entry point for the media recovery
// kernel.
//
// It generates the fixtures, runs them through the reusable packages and prints
// the numbers a reviewer needs: output hashes, pixel mean, loudness peak, packet
// loss and recovery counts, the first unreadable offset and the maximum
// alignment error.
//
// Exit codes: 0 success, 1 a sample assertion failed, 2 bad parameters.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"text/tabwriter"
	"time"

	"mediarc/demo"
	"mediarc/model"
)

const (
	exitOK        = 0
	exitFailed    = 1
	exitBadParams = 2
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return exitBadParams
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		usage(stdout)
		return exitOK
	default:
		fmt.Fprintf(stderr, "mediarc: unknown command %q\n\n", args[0])
		usage(stderr)
		return exitBadParams
	}
}

func usage(w io.Writer) {
	fmt.Fprint(w, `mediarc - local media recovery kernel acceptance harness

usage:
  mediarc demo [flags]

flags:
  --seed int         fixture seed, reported for reproduction (default 7)
  --window int       RTP jitter window depth in packets (default 4)
  --max-buffer int   per-stage pipeline capacity (default 8)
  --out string       output directory (default "out")

The demo generates subtitles, MP4 files and RTP streams locally, runs them
through the recovery kernel and prints the acceptance numbers.

exit codes: 0 success, 1 sample assertion failed, 2 bad parameters
`)
}

func runDemo(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("mediarc demo", flag.ContinueOnError)
	flags.SetOutput(stderr)
	seed := flags.Int64("seed", 7, "fixture seed, reported for reproduction")
	window := flags.Int("window", 4, "RTP jitter window depth in packets")
	maxBuffer := flags.Int("max-buffer", 8, "per-stage pipeline capacity")
	out := flags.String("out", "out", "output directory for fixtures and the manifest")
	if err := flags.Parse(args); err != nil {
		return exitBadParams
	}
	if flags.NArg() > 0 {
		fmt.Fprintf(stderr, "mediarc demo: unexpected argument %q\n", flags.Arg(0))
		return exitBadParams
	}
	if *window <= 0 || *maxBuffer <= 0 {
		fmt.Fprintf(stderr, "mediarc demo: --window and --max-buffer must be positive\n")
		return exitBadParams
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	started := time.Now()
	report, err := demo.Run(ctx, demo.Options{
		Dir:       *out,
		Seed:      *seed,
		Window:    *window,
		MaxBuffer: *maxBuffer,
	})
	if err != nil {
		fmt.Fprintf(stderr, "mediarc demo: %v\n", err)
		if isBadParams(err) {
			return exitBadParams
		}
		return exitFailed
	}
	printReport(stdout, report, *seed, *window, *maxBuffer, time.Since(started))
	if !report.OK() {
		return exitFailed
	}
	return exitOK
}

func isBadParams(err error) bool {
	var modelErr *model.Error
	if errors.As(err, &modelErr) {
		switch modelErr.Code {
		case model.CodeInvalidArgument, model.CodeInvalidTimecode, model.CodeIllegalDropFrame:
			return true
		}
	}
	return false
}

func printReport(w io.Writer, report demo.Report, seed int64, window, maxBuffer int, elapsed time.Duration) {
	fmt.Fprintf(w, "mediarc demo  go=%s  %s/%s\n", runtime.Version(), runtime.GOOS, runtime.GOARCH)
	fmt.Fprintf(w, "seed=%d window=%d max-buffer=%d output=%s\n\n", seed, window, maxBuffer, outputDir(report))

	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	for _, scenario := range report.Scenarios {
		fmt.Fprintf(tw, "%s\t%s\n", scenario.Name, scenario.Detail)
	}
	_ = tw.Flush()

	fmt.Fprintln(w)
	for _, scenario := range report.Scenarios {
		failed := 0
		for _, check := range scenario.Checks {
			if !check.OK {
				failed++
			}
		}
		status := "PASS"
		if failed > 0 {
			status = "FAIL"
		}
		fmt.Fprintf(w, "  [%s] %-10s %d/%d checks\n", status, scenario.Name, len(scenario.Checks)-failed, len(scenario.Checks))
	}

	metrics := report.Metrics
	fmt.Fprintln(w)
	fmt.Fprintln(w, "artefacts:")
	fmt.Fprintf(w, "  frame hash        %016x\n", metrics.FrameHash)
	fmt.Fprintf(w, "  segment hash      %016x\n", metrics.SegmentHash)
	fmt.Fprintf(w, "  pixel mean        %.4f\n", metrics.PixelMean)
	fmt.Fprintf(w, "  loudness peak     %.4f dBFS\n", metrics.PeakDBFS)
	fmt.Fprintf(w, "  packets lost      %d\n", metrics.PacketsLost)
	fmt.Fprintf(w, "  packets recovered %d\n", metrics.Recovered)
	fmt.Fprintf(w, "  first bad offset  %d\n", metrics.FirstBadOff)
	fmt.Fprintf(w, "  max align error   %d ns\n", metrics.MaxAlignErr)
	fmt.Fprintf(w, "  cue checksum      %016x\n", metrics.CueChecksum)
	fmt.Fprintf(w, "  frames/segments   %d/%d\n", metrics.FrameCount, metrics.SegmentCount)
	fmt.Fprintf(w, "  manifest          %s\n", report.Manifest)
	fmt.Fprintf(w, "  elapsed           %s\n", elapsed.Round(time.Millisecond))

	if !report.OK() {
		fmt.Fprintln(w)
		fmt.Fprintln(w, "failed assertions:")
		for _, failure := range report.Failures {
			fmt.Fprintf(w, "  - %s\n", failure)
		}
		fmt.Fprintln(w)
		fmt.Fprintln(w, "RESULT FAIL")
		return
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "RESULT PASS")
}

func outputDir(report demo.Report) string {
	if report.Manifest == "" {
		return ""
	}
	return filepath.Dir(report.Manifest)
}
