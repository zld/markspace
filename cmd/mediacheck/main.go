// Command mediacheck is the offline acceptance entry point for the media
// processing kernel. The demo subcommand generates self-contained MP4/MOV and
// RTP fixtures, runs the full demux->mapping->recovery->fan-in->policy->remux
// pipeline, and prints the evidence fields used to sign off a delivery.
//
// Exit codes:
//
//	0 - every scenario passed its business verification
//	1 - one or more scenarios failed business verification
//	2 - bad arguments or an operational/runtime failure
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"mediacheck/internal/pipeline"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(pipeline.ExitRuntime)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "-h", "--help", "help":
		usage()
		os.Exit(pipeline.ExitOK)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(pipeline.ExitRuntime)
	}
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	seed := fs.Uint64("seed", 1, "deterministic fixture seed")
	budgetMS := fs.Int64("budget-ms", 5000, "per-asset processing budget in milliseconds")
	maxBytes := fs.Int64("max-bytes", 2_000_000, "delivery size ceiling in bytes")
	workDir := fs.String("workdir", "", "artifact directory (default ./.mediacheck-demo)")
	if err := fs.Parse(args); err != nil {
		return pipeline.ExitRuntime
	}
	if *budgetMS < 0 || *maxBytes < 0 {
		fmt.Fprintln(os.Stderr, "budget-ms and max-bytes must be non-negative")
		return pipeline.ExitRuntime
	}
	dir := *workDir
	if dir == "" {
		dir = filepath.Join(".", ".mediacheck-demo")
	}

	results, err := pipeline.RunDemo(pipeline.Options{
		Seed: *seed, BudgetMS: *budgetMS, MaxBytes: *maxBytes, WorkDir: dir,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "runtime error: %v\n", err)
		return pipeline.ExitRuntime
	}

	printResults(results)

	for _, r := range results {
		if !r.OK {
			return pipeline.ExitBusiness
		}
	}
	return pipeline.ExitOK
}

func printResults(results []pipeline.ScenarioResult) {
	for _, r := range results {
		status := "PASS"
		if !r.OK {
			status = "FAIL"
		}
		fmt.Printf("===== scenario=%s status=%s decision=%s =====\n",
			r.Name, status, r.Decision)
		fmt.Printf("input_sha256=%s\n", dash(r.InputSHA256))
		fmt.Printf("output_sha256=%s\n", dash(r.OutputSHA256))
		fmt.Printf("frame_hash=%s\n", dash(r.FrameHash))
		fmt.Printf("pixel_mean=%.4f pixel_stddev=%.4f\n", r.PixelMean, r.PixelStddev)
		fmt.Printf("audio_peak_dbfs=%s\n", pipeline.FormatDBFS(r.AudioPeakDBFS))
		fmt.Printf("start_error_ticks=%d end_error_ticks=%d\n",
			r.StartErrorTicks, r.EndErrorTicks)
		fmt.Printf("recovered_layers=%d\n", r.RecoveredLayers)
		if r.ErrorCode != "" {
			fmt.Printf("error_code=%s error_offset=%d error_tick=%d/%dHz\n",
				r.ErrorCode, r.ErrorOffset, r.ErrorTick, r.ErrorTimescale)
			fmt.Printf("error_detail=%s\n", r.Note)
		}
		fmt.Println()
	}
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func usage() {
	fmt.Fprint(os.Stderr, `mediacheck - offline media delivery verification

Usage:
  mediacheck demo [--seed N] [--budget-ms N] [--max-bytes N] [--workdir DIR]

The demo generates healthy, truncated, RTP-wrap/recovery and boundary-
misalignment fixtures and prints evidence per scenario.

Exit codes: 0 all pass, 1 business verification failure, 2 argument/runtime error.
`)
}
