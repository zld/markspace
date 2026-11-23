package main

import (
	"fmt"
	"os"
	"path/filepath"

	"mediaroute/internal/journal"
	"mediaroute/internal/pipeline"
)

func runVerify(args []string) int {
	flags, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return pipeline.ExitBadArguments
	}
	repeat, err := atoiFlag(flags, "repeat", 2)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return pipeline.ExitBadArguments
	}
	if repeat < 2 {
		fmt.Fprintln(os.Stderr, "argument error: --repeat must be >= 2")
		return pipeline.ExitBadArguments
	}
	opts, err := buildOptions("verify", flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return pipeline.ExitBadArguments
	}
	// First, prove recovery over any pre-existing journal state, then run the
	// first pass over that recovered state so "rerun after recovery" is the
	// exact comparison the acceptance criteria ask for.
	report, err := journal.Recover(opts.JournalDir)
	if err != nil {
		return failWith(err)
	}
	hashes := make([]string, 0, repeat)
	var firstMetrics *pipeline.Metrics
	for i := 0; i < repeat; i++ {
		runOpts := opts
		runOpts.CrashAt = ""
		if i > 0 {
			runOpts.JournalDir = filepath.Join(opts.JournalDir, fmt.Sprintf("run-%d", i))
		}
		res, err := runOnce(runOpts)
		if err != nil {
			return failWith(err)
		}
		hashes = append(hashes, res.hash)
		if i == 0 {
			firstMetrics = res.metrics
		}
	}
	match := true
	for i := 1; i < len(hashes); i++ {
		match = match && hashes[i] == hashes[0]
	}
	recovered := len(report.Replayed) > 0 || report.Isolated
	firstMetrics.Recovered = recovered
	firstMetrics.RepeatHashMatch = match
	printMetrics(firstMetrics, nil)
	fmt.Printf("repeat_runs=%d\n", repeat)
	for i, h := range hashes {
		fmt.Printf("manifest_sha256[%d]=%s\n", i, h)
	}
	if !match {
		fmt.Println(`status=failed phase=export recoverable=false error="repeat hash mismatch"`)
		return pipeline.ExitRecoveryFailure
	}
	if !recovered {
		fmt.Println("status=ok note=\"no prior journal state to recover; consistency verified\"")
		return pipeline.ExitOK
	}
	fmt.Println("status=ok")
	return pipeline.ExitOK
}
