// Command mediad is the offline media-pipeline acceptance entry point.
// It generates its own fixtures locally, runs the full data flow and prints
// a machine-readable summary. No network or GUI dependencies are used.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"mediad/engine"
)

const bitrateTolerancePct = 5.0

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	switch args[0] {
	case "demo":
		return runDemo(args[1:])
	case "help", "-h", "--help":
		usage()
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", args[0])
		usage()
		return 2
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	duration := fs.Duration("duration", 12*time.Second, "timeline duration (e.g. 12s)")
	fps := fs.Int("fps", 25, "frames per second (1..30)")
	target := fs.Int64("target-bitrate", 2_400_000, "target bitrate in bps")
	queueCap := fs.Int("queue-capacity", 8, "bounded queue capacity in samples")
	consumerDelay := fs.Duration("consumer-delay", 0, "artificial per-frame consumer delay")
	fixture := fs.String("fixture", "", "fault fixture: truncated|corrupt")
	recover := fs.Bool("recover", false, "resume from the last complete checkpoint")
	out := fs.String("out", filepath.Join(os.TempDir(), "mediad-demo"), "output directory")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *fixture != "" && *fixture != "truncated" && *fixture != "corrupt" {
		fmt.Fprintf(os.Stderr, "invalid --fixture %q (want truncated|corrupt)\n", *fixture)
		return 2
	}
	// Argument validation happens here so misuse yields exit code 2, distinct
	// from input/verification failures (exit code 1).
	if *fps <= 0 || *fps > 30 {
		fmt.Fprintf(os.Stderr, "invalid --fps %d: must be in 1..30\n", *fps)
		return 2
	}
	if *duration <= 0 {
		fmt.Fprintln(os.Stderr, "invalid --duration: must be positive")
		return 2
	}
	if *target <= 0 {
		fmt.Fprintln(os.Stderr, "invalid --target-bitrate: must be positive")
		return 2
	}
	if *queueCap <= 0 {
		fmt.Fprintln(os.Stderr, "invalid --queue-capacity: must be positive")
		return 2
	}
	cfg := engine.Config{
		Duration: *duration, FPS: *fps, TargetBitrate: *target,
		QueueCapacity: *queueCap, ConsumerDelay: *consumerDelay,
		OutputDir: *out, Fixture: *fixture, Recover: *recover,
		BitrateTolPct: bitrateTolerancePct,
	}
	rep, err := engine.Run(context.Background(), cfg)
	if err != nil {
		// Input/verification failures are exit code 1; bad args were handled above.
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		return 1
	}
	printReport(rep)
	// Non-zero exit when the achieved bitrate misses target beyond tolerance.
	if !withinTolerance(rep.BitrateDeltaPct, bitrateTolerancePct) {
		fmt.Fprintf(os.Stderr, "bitrate deviation %.3f%% exceeds %.0f%% threshold\n",
			rep.BitrateDeltaPct, bitrateTolerancePct)
		return 1
	}
	return 0
}

func printReport(r *engine.Report) {
	fmt.Printf("frames=%d\n", r.Frames)
	fmt.Printf("target_bps=%d\n", r.TargetBPS)
	fmt.Printf("actual_bps=%d\n", r.ActualBPS)
	fmt.Printf("bitrate_delta_pct=%.3f\n", r.BitrateDeltaPct)
	fmt.Printf("quality_mean=%.4f\n", r.QualityMean)
	fmt.Printf("quality_variance=%.4f\n", r.QualityVariance)
	fmt.Printf("manifest_max_error_ns=%d\n", r.ManifestMaxErr)
	fmt.Printf("manifest_sha256=%s\n", r.ManifestSHA)
	fmt.Printf("manifest_state=%s\n", r.State)
	if len(r.Errors) > 0 {
		fmt.Printf("errors=%d\n", len(r.Errors))
		for _, e := range r.Errors {
			fmt.Printf("  error track=%d time_ns=%d kind=%s reason=%s\n",
				e.TrackID, e.TimeNS, e.Kind, e.Reason)
		}
	}
	if r.RecoveredFrames > 0 {
		fmt.Printf("recovered_frames=%d\n", r.RecoveredFrames)
	}
	if r.FirstBadTimeNS > 0 {
		fmt.Printf("first_bad_time_ns=%d\n", r.FirstBadTimeNS)
	}
	fmt.Printf("manifest_path=%s\n", r.ManifestPath)
}

func withinTolerance(delta, threshold float64) bool {
	if delta < 0 {
		delta = -delta
	}
	return delta <= threshold
}

func usage() {
	fmt.Fprint(os.Stderr, `mediad - offline multi-track media export kernel

usage:
  mediad demo [flags]

flags:
  --duration D         timeline duration, e.g. 12s (default 12s)
  --fps N              frames per second, 1..30 (default 25)
  --target-bitrate B   target bitrate in bps (default 2400000)
  --queue-capacity N   bounded queue capacity in samples (default 8)
  --consumer-delay D   artificial slow-consumer delay per frame (default 0)
  --fixture NAME       fault fixture: truncated | corrupt
  --recover            resume from the last complete frame checkpoint
  --out DIR            output directory for manifest and checkpoint

exit codes: 0 success within bitrate tolerance, 1 input/verify failure, 2 bad arguments
`)
}
