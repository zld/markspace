// Package demo wires every subsystem into the end-to-end media pipeline used by
// the cmd/mediapipeline acceptance entry point.  Keeping the orchestration here
// (rather than in main) lets the whole pipeline be exercised by tests with the
// same code paths and exit-code semantics as the CLI.
package demo

import (
	"context"
	"strings"

	"mediapipeline/internal/fixture"
	"mediapipeline/internal/ingest"
)

// Config is one demo invocation.
type Config struct {
	Codec          ingest.Codec
	Seed           int64
	BandwidthBps   int64 // 0 means use the fixture default
	Injections     []string
	InterruptAfter int // 0 disables interruption
	Resume         bool
	StateDir       string // checkpoint/staging directory
}

// DamageLine is one human-readable damage or validation record.
type DamageLine struct {
	Offset int64
	Code   string
	Msg    string
}

// Result is the full pipeline outcome.
type Result struct {
	Assets           int
	Missing          int
	Conflicts        int
	ValidatorPass    bool
	SHA256           string
	Gaps             int
	BudgetRejections int
	Damage           []DamageLine
	ValidationFaults []DamageLine
	RolledBack       bool
	RollbackReason   string

	// Resume bookkeeping.
	Resumed        bool
	ResumedUnits   int
	InterruptedRun bool
	ElapsedMS      int64

	// InputDamage is true when the generated input itself was damaged, as
	// opposed to a clean run whose output validation failed.
	InputDamage bool
}

// ExitCode maps the outcome to the documented process exit code:
// 0 validation passed, 2 damaged input fully reported, 1 budget/output failure.
func (r *Result) ExitCode() int {
	switch {
	case r.ValidatorPass && r.BudgetRejections == 0:
		return 0
	case r.InputDamage:
		return 2
	default:
		return 1
	}
}

// ParseInjections splits a comma-separated injection list, dropping blanks.
func ParseInjections(s string) []string {
	var out []string
	for _, name := range strings.Split(s, ",") {
		if name = strings.TrimSpace(name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// Run executes the pipeline.  It returns a Result describing everything that
// happened; the caller decides the exit code.  An error is only returned for
// setup failures (bad config, filesystem), never for reported damage.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	sample, err := fixture.Generate(cfg.Codec, cfg.Seed, cfg.Injections)
	if err != nil {
		return nil, err
	}
	if cfg.BandwidthBps > 0 {
		sample.Bandwidth.NormalBps = cfg.BandwidthBps
	}
	return runPipeline(ctx, cfg, sample)
}
