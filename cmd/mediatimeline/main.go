// Command mediatimeline is the acceptance entry point. It only parses flags
// and drives the reusable pipeline library; all media logic lives in packages.
//
// Exit codes:
//
//	0 - completed (asset failures may still be reported in errors.ndjson)
//	1 - material or processing error with a stable error record emitted
//	2 - invalid command-line parameters
package main

import (
	"flag"
	"fmt"
	"math"
	"os"

	"mediatimeline/evidence"
	"mediatimeline/internal/pipeline"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage()
		return 2
	}
	if args[0] != "demo" {
		fmt.Fprintf(os.Stderr, "unknown command %q (want: demo)\n", args[0])
		return 2
	}
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	var (
		seed      = fs.Int64("seed", 1, "deterministic fixture seed")
		out       = fs.String("out", "", "output directory (required)")
		corrupt   = fs.String("corrupt", "", "corruption mode: truncated")
		lateEvent = fs.Int64("late-event", 0, "delay one client event by this ns hint")
		driftPPM  = fs.Float64("drift-ppm", 50, "injected client clock drift, ppm")
		driftOff  = fs.Int64("drift-offset", 2_000_000, "fixed client clock offset, ns")
	)
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	if *out == "" {
		fmt.Fprintln(os.Stderr, "missing required --out")
		return 2
	}
	if *corrupt != "" && *corrupt != "truncated" {
		fmt.Fprintf(os.Stderr, "unsupported --corrupt mode %q\n", *corrupt)
		return 2
	}

	cfg := pipeline.Config{
		Seed: *seed, OutDir: *out, Corrupt: *corrupt,
		LateEventNS: *lateEvent, DriftRatePPM: *driftPPM, DriftOffsetNS: *driftOff,
	}
	res, err := pipeline.Run(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "run failed: %v\n", err)
		return 1
	}
	printSummary(res)
	// A run still completes (exit 0) when individual assets fail, as long as
	// the pipeline produced a stable summary; the failures are machine-readable.
	return 0
}

func printSummary(r *pipeline.Result) {
	fmt.Printf("seed=%d\n", r.Seed)
	fmt.Printf("manifest_segments=%d\n", r.ManifestSegments)
	fmt.Printf("merged_intervals=%d\n", r.MergedIntervals)
	fmt.Printf("gap_count=%d conflict_count=%d\n", r.Gaps, r.Conflicts)
	fmt.Printf("valid_frames=%d\n", r.ValidFrames)
	for _, h := range r.FrameHashes {
		fmt.Printf("frame_sha256=%s\n", h)
	}
	fmt.Printf("pixel_mean_rgb=%.4f,%.4f,%.4f\n", r.PixelMeanRGB[0], r.PixelMeanRGB[1], r.PixelMeanRGB[2])
	if r.AudioSilence || math.IsInf(r.AudioPeakDBFS, -1) {
		fmt.Println("audio_peak_dbfs=-inf")
	} else {
		fmt.Printf("audio_peak_dbfs=%s\n", evidence.FormatDBFS(r.AudioPeakDBFS))
	}
	fmt.Printf("alignment_error_ns=%d\n", r.AlignmentErrorNS)
	fmt.Printf("watermark_ns=%d\n", r.WatermarkNS)
	fmt.Printf("replay_digest=%s\n", r.ReplayDigest)
	fmt.Printf("late_replay_count=%d\n", r.LateReplayCount)
	if r.RollbackFromNS > 0 {
		fmt.Printf("rollback_from_ns=%d\n", r.RollbackFromNS)
	}
	if len(r.Failures) > 0 {
		fmt.Printf("failures=%d (see errors.ndjson)\n", len(r.Failures))
		for _, f := range r.Failures {
			if f.ByteOffset > 0 {
				fmt.Printf("  %s stage=%s asset=%s byte_offset=%d\n", f.Code, f.Stage, f.Asset, f.ByteOffset)
			} else {
				fmt.Printf("  %s stage=%s asset=%s\n", f.Code, f.Stage, f.Asset)
			}
		}
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, "usage: mediatimeline demo --seed N --out DIR [--corrupt truncated] [--late-event NS]")
}
