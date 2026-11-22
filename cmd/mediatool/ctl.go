package main

import (
	"fmt"
	"os"
)

// demo executes one full pipeline run and prints acceptance metrics.
func demo(cfg cliConfig) int {
	rep, err := RunOnce(runConfig{
		Seed: cfg.Seed, Tracks: cfg.Tracks, Workers: cfg.Workers, Faults: cfg.Faults,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo: %v\n", err)
		return exitFailed
	}
	rep.Print()
	if !rep.ThresholdsOK() {
		return exitFailed
	}
	return exitOK
}

// verify runs the pipeline repeatedly and checks determinism + invariants.
func verify(cfg cliConfig) int {
	rc := runConfig{Seed: cfg.Seed, Tracks: cfg.Tracks, Workers: cfg.Workers, Faults: cfg.Faults}
	first, err := RunOnce(rc)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify: %v\n", err)
		return exitFailed
	}
	deterministic := true
	for i := 1; i < cfg.Runs; i++ {
		next, err := RunOnce(rc)
		if err != nil {
			fmt.Fprintf(os.Stderr, "verify: %v\n", err)
			return exitFailed
		}
		if next.OutputSHA256 != first.OutputSHA256 {
			deterministic = false
		}
	}
	fmt.Printf("deterministic=%v\n", deterministic)
	fmt.Printf("output_sha256=%x\n", first.OutputSHA256)
	passed, total := first.Invariants(deterministic)
	fmt.Printf("invariants_passed=%d/%d\n", passed, total)
	if !deterministic || passed != total {
		return exitFailed
	}
	return exitOK
}
