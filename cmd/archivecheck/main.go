// Command archivecheck is the terminal acceptance adapter for the reusable
// media archive processing kernel.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"archivecheck/internal/pipeline"
	"archivecheck/internal/report"
	"archivecheck/internal/samplegen"
)

const (
	exitOK           = 0
	exitExpectedFail = 2
	exitUsage        = 64
	exitInternal     = 1
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		printUsage(stdout)
		return exitOK
	}
	if args[0] != "demo" {
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return exitUsage
	}

	fs := flag.NewFlagSet("demo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	seed := fs.Int64("seed", 17, "deterministic sample seed")
	workers := fs.Int("workers", 3, "bounded worker count (1..8)")
	budgetBytes := fs.Int64("budget-bytes", 1<<20, "total/per-item output budget in bytes")
	cancelAfter := fs.Int("cancel-after", 0, "cancel demonstration run after this many completed items")
	if err := fs.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "unexpected arguments: %v\n", fs.Args())
		return exitUsage
	}
	if *workers < 1 || *workers > pipeline.MaxWorkers {
		fmt.Fprintf(stderr, "--workers must be in [1,%d]\n", pipeline.MaxWorkers)
		return exitUsage
	}
	if *budgetBytes < 0 {
		fmt.Fprintln(stderr, "--budget-bytes must be non-negative")
		return exitUsage
	}
	if *cancelAfter < 0 {
		fmt.Fprintln(stderr, "--cancel-after must be non-negative")
		return exitUsage
	}

	samples := samplegen.All(*seed, *budgetBytes)
	jobs := BuildJobs(samples, *seed)
	opts := pipeline.Options{Workers: *workers, CancelAfterCompleted: *cancelAfter}

	if *cancelAfter > 0 {
		return runCancelDemo(stdout, stderr, samples, jobs, *seed, *budgetBytes, *workers, *cancelAfter)
	}

	results := pipeline.Run(context.Background(), jobs, opts)
	if err := pipeline.VerifyBatch(results, len(jobs)); err != nil {
		fmt.Fprintf(stderr, "pipeline merge error: %v\n", err)
		return exitInternal
	}
	summary := report.Build(*seed, *budgetBytes, results)
	fmt.Fprint(stdout, summary.FormatTerminal())
	if !summary.BudgetWithin {
		fmt.Fprintf(stderr, "budget exceeded: output %d > %d\n", summary.TotalOutputBytes, *budgetBytes)
		return exitInternal
	}
	if summary.Counts.Failed > 0 {
		return exitExpectedFail
	}
	return exitOK
}

func runCancelDemo(stdout, stderr io.Writer, samples []samplegen.Sample, jobs []pipeline.Job,
	seed, budget int64, workers, cancelAfter int) int {

	firstResults := pipeline.Run(context.Background(), BarrierAfter(jobs, cancelAfter), pipeline.Options{
		Workers: workers, CancelAfterCompleted: cancelAfter,
	})
	firstCounts := pipeline.Count(firstResults)
	fmt.Fprintf(stdout, "cancel_run total=%d completed=%d canceled=%d not_started=%d\n",
		firstCounts.Total, firstCounts.Completed, firstCounts.Canceled, firstCounts.NotStarted)
	if firstCounts.Canceled == 0 || firstCounts.NotStarted == 0 {
		fmt.Fprintln(stderr, "cancellation demo did not produce both canceled and not-started items")
		return exitInternal
	}

	rerunJobs := pipeline.RerunJobs(jobs, firstResults)
	rerunResults := pipeline.Run(context.Background(), rerunJobs, pipeline.Options{Workers: workers})
	merged := pipeline.MergeResume(firstResults, rerunResults)
	full := pipeline.Run(context.Background(), jobs, pipeline.Options{Workers: workers})

	if err := pipeline.VerifyBatch(merged, len(samples)); err != nil {
		fmt.Fprintf(stderr, "resume merge error: %v\n", err)
		return exitInternal
	}
	summary := report.Build(seed, budget, merged)
	fullSummary := report.Build(seed, budget, full)
	fmt.Fprint(stdout, summary.FormatTerminal())
	fmt.Fprintf(stdout, "rerun_jobs=%d resume_digest=%s\n", len(rerunJobs), summary.StableDigest)
	fmt.Fprintf(stdout, "full_digest=%s\n", fullSummary.StableDigest)
	if summary.StableDigest != fullSummary.StableDigest {
		fmt.Fprintln(stderr, "resumed summary does not match full run")
		return exitInternal
	}
	fmt.Fprintln(stdout, "resume_matches_full=true")
	if !summary.BudgetWithin {
		fmt.Fprintf(stderr, "budget exceeded after resume: %d > %d\n", summary.TotalOutputBytes, budget)
		return exitInternal
	}
	if summary.Counts.Failed > 0 {
		return exitExpectedFail
	}
	return exitOK
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, "usage: archivecheck demo [--seed N] [--workers N] [--budget-bytes N] [--cancel-after N]")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Runs the code-generated archive verification sample set.")
	fmt.Fprintln(w, "Exit codes: 0 success, 2 expected sample failures, 64 usage error, 1 internal error.")
}
