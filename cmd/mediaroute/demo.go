package main

import (
	"fmt"
	"os"

	"mediaroute/internal/clock"
	"mediaroute/internal/export"
	"mediaroute/internal/journal"
	"mediaroute/internal/pipeline"
)

func runDemo(args []string) int {
	flags, err := parseFlags(args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return pipeline.ExitBadArguments
	}
	opts, err := buildOptions("demo", flags)
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		return pipeline.ExitBadArguments
	}
	if opts.CrashAt == "" {
		return runDemoClean(opts)
	}
	return runDemoRecovered(opts)
}

// runResult carries the evidence one pipeline pass produced.
type runResult struct {
	metrics  *pipeline.Metrics
	manifest *export.Manifest
	hash     string
}

// runOnce executes the pipeline in opts.JournalDir and returns its evidence.
func runOnce(opts pipeline.Options) (*runResult, error) {
	runner, err := pipeline.NewRunner(opts)
	if err != nil {
		return nil, err
	}
	metrics, err := runner.Run()
	if err != nil {
		return nil, err
	}
	m := runner.Manifest()
	hash, err := m.ContentHash()
	if err != nil {
		return nil, err
	}
	return &runResult{metrics: metrics, manifest: m, hash: hash}, nil
}

// runDemoClean is the ordinary path: one pass, then one independent repeat to
// prove the same input yields the same manifest hash.
func runDemoClean(opts pipeline.Options) int {
	first, err := runOnce(opts)
	if err != nil {
		return failWith(err)
	}
	repeatOpts := opts
	repeatOpts.JournalDir = opts.JournalDir + "-repeat"
	if err := pipeline.Cleanup(repeatOpts.JournalDir); err != nil {
		fmt.Printf("status=failed phase=journal recoverable=true error=%q\n",
			"cannot reset repeat journal directory: "+err.Error())
		return pipeline.ExitRecoveryFailure
	}
	second, err := runOnce(repeatOpts)
	if err != nil {
		return failWith(err)
	}
	first.metrics.RepeatHashMatch = first.hash == second.hash
	printMetrics(first.metrics, first.manifest)
	fmt.Printf("manifest_sha256=%s\n", first.hash)
	if !first.metrics.RepeatHashMatch {
		fmt.Println(`status=failed phase=export recoverable=false error="repeat hash mismatch"`)
		return pipeline.ExitRecoveryFailure
	}
	fmt.Println("status=ok")
	return pipeline.ExitOK
}

// runDemoRecovered demonstrates the durability contract end to end: it starts
// from a clean journal, runs until the requested simulated crash, recovers
// the committed prefix from the WAL, then reruns to completion over the
// recovered state. A second independent rerun proves the recovered rerun is
// deterministic, so the printed hash and metrics are evidence, not assertion.
func runDemoRecovered(opts pipeline.Options) int {
	// A crash demo must start from a clean journal so recovery is visible.
	if err := pipeline.Cleanup(opts.JournalDir); err != nil {
		fmt.Printf("status=failed phase=journal recoverable=true error=%q\n",
			"cannot reset journal directory: "+err.Error())
		return pipeline.ExitRecoveryFailure
	}
	runner, err := pipeline.NewRunner(opts)
	if err != nil {
		return failWith(err)
	}
	_, runErr := runner.Run()
	if runErr == nil {
		fmt.Printf("status=failed phase=journal recoverable=false crash_at=%s error=%q\n",
			opts.CrashAt, "configured crash point was never reached")
		return pipeline.ExitRecoveryFailure
	}
	pe, ok := clock.AsError(runErr)
	if !ok || pe.Phase != clock.PhaseJournal {
		// Only an injected journal crash is expected here; anything else is a
		// real failure and must keep its own exit code.
		return failWith(runErr)
	}
	fmt.Printf("crash_at=%s crash_phase=%s crash_track=%d crash_recoverable=%t\n",
		opts.CrashAt, pe.Phase, pe.Track, pe.Recoverable)

	report, err := journal.Recover(opts.JournalDir)
	if err != nil {
		return failWith(err)
	}
	fmt.Printf("recovery_replayed=%d recovery_isolated=%t recovery_tail_offset=%d\n",
		len(report.Replayed), report.Isolated, report.TailOffset)

	// Rerun over the recovered journal, then once more from scratch.
	rerunOpts := opts
	rerunOpts.CrashAt = ""
	recoveredRun, err := runOnce(rerunOpts)
	if err != nil {
		return failWith(err)
	}
	repeatOpts := rerunOpts
	repeatOpts.JournalDir = opts.JournalDir + "-repeat"
	if err := pipeline.Cleanup(repeatOpts.JournalDir); err != nil {
		fmt.Printf("status=failed phase=journal recoverable=true error=%q\n",
			"cannot reset repeat journal directory: "+err.Error())
		return pipeline.ExitRecoveryFailure
	}
	repeatRun, err := runOnce(repeatOpts)
	if err != nil {
		return failWith(err)
	}
	m := recoveredRun.metrics
	m.Recovered = true
	m.RepeatHashMatch = recoveredRun.hash == repeatRun.hash
	printMetrics(m, recoveredRun.manifest)
	fmt.Printf("manifest_sha256=%s\n", recoveredRun.hash)
	if !m.RepeatHashMatch {
		fmt.Println(`status=failed phase=export recoverable=false error="recovery rerun hash mismatch"`)
		return pipeline.ExitRecoveryFailure
	}
	fmt.Println("status=ok")
	return pipeline.ExitOK
}
