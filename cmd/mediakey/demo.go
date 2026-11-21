package main

import (
	"fmt"
	"io"

	"mediakey/internal/fixture"
	"mediakey/internal/model"
	"mediakey/internal/pipeline"
	"mediakey/internal/report"
)

// runDemo generates an in-memory sample, injects the scenario faults, runs
// the full pipeline and verifies the counters against the injection plan.
func runDemo(args []string, stdout, stderr io.Writer) int {
	fs := parseFlagSet("demo", stderr)
	scenario := fs.String("scenario", "mixed", "ok|mixed|chaos")
	shards := fs.Int("shards", 12, "number of media shards to generate")
	rotateEvery := fs.Int("rotate-every", 4, "shards per key version")
	workers := fs.Int("workers", 3, "parallel verification workers")
	reportPath := fs.String("report", "", "optional JSON report output path")
	seed := fs.Int("seed", 14641, "deterministic sample seed")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || *shards < 4 || *rotateEvery < 1 || *workers < 1 {
		fs.Usage()
		return exitUsage
	}
	if !validScenario(*scenario) {
		fmt.Fprintf(stderr, "invalid scenario %q (want ok, mixed or chaos)\n", *scenario)
		return exitUsage
	}

	sample := fixture.BuildSample("season-package-01", *shards, *seed)
	sample.ApplyScenario(*scenario)

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
	if err := verifyExpectations(rep, *scenario); err != nil {
		fmt.Fprintf(stderr, "expectation mismatch: %v\n", err)
		return exitMismatch
	}
	return exitOK
}

// validScenario reports whether name is one of the built-in scenarios.
func validScenario(name string) bool {
	return name == "ok" || name == "mixed" || name == "chaos"
}

// verifyExpectations checks aggregate counters and the per-reason coverage
// promised by each scenario.
func verifyExpectations(rep *model.Report, scenario string) error {
	if len(rep.Results) != rep.ShardCount {
		return fmt.Errorf("result rows %d != shard_count %d", len(rep.Results), rep.ShardCount)
	}
	seen := map[string]bool{}
	for _, r := range rep.Results {
		seen[r.FailureReason] = true
		if r.Status == model.StatusAccepted && r.FailureReason != model.ReasonNone {
			return fmt.Errorf("shard %d accepted with reason %s", r.ShardID, r.FailureReason)
		}
		if r.KeyID == "" {
			return fmt.Errorf("shard %d missing key id", r.ShardID)
		}
	}
	switch scenario {
	case "ok":
		if rep.Rejected != 0 || rep.Accepted != rep.ShardCount {
			return fmt.Errorf("ok scenario expects all accepts, got %d rejected", rep.Rejected)
		}
	case "mixed":
		for _, reason := range []string{
			model.ReasonKeyExpired,
			model.ReasonLicenseExpired,
			model.ReasonLicenseReplay,
			model.ReasonAuthTag,
		} {
			if !seen[reason] {
				return fmt.Errorf("mixed scenario missing failure reason %s", reason)
			}
		}
		if rep.Rejected != 4 || rep.Accepted != rep.ShardCount-4 {
			return fmt.Errorf("mixed scenario expects 4 rejects, got %d", rep.Rejected)
		}
	case "chaos":
		if rep.Rejected == 0 {
			return fmt.Errorf("chaos scenario expects at least one rejection")
		}
	}
	return nil
}
