// Command audionorm is the terminal front-end for the reusable audio
// normalization kernel. The demo subcommand generates all of its own
// inputs (no external assets), exercises every failure and recovery
// path, and exits non-zero if any acceptance assertion fails.
package main

import (
	"audionorm/internal/demo"
	"context"
	"flag"
	"fmt"
	"os"
	"time"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "demo":
		os.Exit(runDemo(os.Args[2:]))
	case "version":
		fmt.Println("audionorm demo kernel (Go 1.23, FFmpeg >= 7.1)")
		os.Exit(0)
	case "-h", "--help", "help":
		usage()
		os.Exit(0)
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: audionorm demo --work <dir>")
	fmt.Fprintln(os.Stderr, "       audionorm version")
}

func runDemo(args []string) int {
	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	work := fs.String("work", "", "working directory for checkpoints, PCM and manifest")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *work == "" {
		fmt.Fprintln(os.Stderr, "demo: --work is required")
		return 2
	}
	if err := os.MkdirAll(*work, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "cannot create work dir: %v\n", err)
		return 1
	}

	runner, err := demo.NewRunner(*work)
	if err != nil {
		fmt.Fprintf(os.Stderr, "init failed: %v\n", err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	results, err := runner.RunAll(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo infrastructure error: %v\n", err)
		return 1
	}

	allPass := true
	fmt.Println("== audionorm demo ==")
	for _, r := range results {
		mark := "PASS"
		if !r.OK {
			mark = "FAIL"
			allPass = false
		}
		fmt.Printf("\n[%s] scenario=%s status=%s threshold=%s\n", mark, r.Name, r.Status, r.Threshold)
		printScenario(r)
	}
	fmt.Println("\n== manifest ==")
	if len(results) > 0 {
		fmt.Printf("rerun_manifest_sha256=%s\n", results[0].ManifestSHA)
	}
	if allPass {
		fmt.Println("\nALL CHECKS PASSED")
		return 0
	}
	fmt.Println("\nONE OR MORE CHECKS FAILED")
	return 1
}

func printScenario(r demo.ScenarioResult) {
	if r.Samples != 0 {
		fmt.Printf("  samples=%d rms=%.2f dBFS peak=%.2f dBFS\n",
			r.Samples, r.OutputRMSDB, r.OutputPeakDB)
	}
	if r.Duplicates != 0 || r.MissingSamples != 0 || r.Interval != "" {
		fmt.Printf("  duplicates=%d missing_samples=%d interval=%s budget_deviation=%.2f%%\n",
			r.Duplicates, r.MissingSamples, r.Interval, r.BudgetDeviation)
	}
	if r.HoldOffset != 0 || r.Status == "HOLD" {
		fmt.Printf("  hold_offset=%d\n", r.HoldOffset)
	}
	if r.CheckpointReused {
		fmt.Println("  checkpoint_reused=true")
	}
	for _, d := range r.Details {
		fmt.Printf("  - %s\n", d)
	}
}
