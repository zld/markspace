package main

import (
	"fmt"
	"io"

	"mediakey/internal/fixture"
	"mediakey/internal/model"
	"mediakey/internal/pipeline"
	"mediakey/internal/report"
)

// runBootstrap materializes a local sample bundle on disk so it can later be
// processed offline by the process subcommand.
func runBootstrap(args []string, stdout, stderr io.Writer) int {
	fs := parseFlagSet("bootstrap", stderr)
	dir := fs.String("dir", "", "output bundle directory (required)")
	shards := fs.Int("shards", 12, "number of media shards")
	scenario := fs.String("scenario", "mixed", "ok|mixed|chaos fault plan")
	seed := fs.Int("seed", 14641, "deterministic sample seed")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *dir == "" || fs.NArg() != 0 || *shards < 1 || !validScenario(*scenario) {
		fs.Usage()
		return exitUsage
	}
	sample := fixture.BuildSample("season-package-01", *shards, *seed)
	sample.ApplyScenario(*scenario)
	if err := sample.Save(*dir); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitInternal
	}
	fmt.Fprintf(stdout, "wrote %d shards to %s\n", *shards, *dir)
	return exitOK
}

// runProcess loads a local bundle and runs the same pipeline as demo against
// bytes read from disk rather than generated in memory.
func runProcess(args []string, stdout, stderr io.Writer) int {
	fs := parseFlagSet("process", stderr)
	dir := fs.String("dir", "", "bundle directory (required)")
	rotateEvery := fs.Int("rotate-every", 4, "shards per key version")
	workers := fs.Int("workers", 3, "parallel verification workers")
	reportPath := fs.String("report", "", "optional JSON report output path")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if *dir == "" || fs.NArg() != 0 || *rotateEvery < 1 || *workers < 1 {
		fs.Usage()
		return exitUsage
	}
	sample, err := fixture.Load(*dir)
	if err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return exitInternal
	}
	runner := pipeline.New(sample.Manifest.PackageID, pipeline.Config{
		Clock:       baseClock(),
		RotateEvery: *rotateEvery,
		Workers:     *workers,
	})
	rep, err := runner.Run(sample)
	if err != nil {
		fmt.Fprintf(stderr, "pipeline error: %v\n", err)
		return exitInternal
	}
	report.Print(stdout, rep)
	if *reportPath != "" {
		if err := report.WriteJSON(*reportPath, rep); err != nil {
			fmt.Fprintf(stderr, "%v\n", err)
			return exitInternal
		}
	}
	if !bundleMatches(rep, sample) {
		fmt.Fprintf(stderr, "expectation mismatch: bundle fault plan not reproduced\n")
		return exitMismatch
	}
	return exitOK
}

// bundleMatches verifies that every injected fault produced its mapped
// reason and every clean shard was accepted.
func bundleMatches(rep *model.Report, sample *fixture.Sample) bool {
	byID := map[int]model.ShardResult{}
	for _, r := range rep.Results {
		byID[r.ShardID] = r
	}
	for _, sh := range sample.Manifest.Shards {
		r, ok := byID[sh.ShardID]
		if !ok {
			return false
		}
		fault := sample.Fault(sh.ShardID)
		if fault == fixture.FaultNone {
			if r.Status != model.StatusAccepted || r.FailureReason != model.ReasonNone {
				return false
			}
			continue
		}
		if r.Status != model.StatusRejected || r.FailureReason != fault {
			return false
		}
	}
	return true
}
