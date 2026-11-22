package pipeline

import (
	"fmt"
	"os"

	"mediq/internal/bitstream"
	"mediq/internal/index"
	"mediq/internal/journal"
	"mediq/internal/manifest"
	"mediq/internal/report"
	"mediq/internal/segment"
	"mediq/internal/timeline"
)

// FailPoint selects where an injected crash stops the run.
type FailPoint string

const (
	FailNone     FailPoint = ""
	FailAfterScan FailPoint = "scan"
	FailAfterPlan FailPoint = "plan"
	FailBeforeCommit FailPoint = "commit"
)

// ParseFailPoint maps the CLI --fail-after value.
func ParseFailPoint(s string) (FailPoint, error) {
	switch s {
	case "", "none":
		return FailNone, nil
	case "scan":
		return FailAfterScan, nil
	case "plan":
		return FailAfterPlan, nil
	case "commit":
		return FailBeforeCommit, nil
	default:
		return FailNone, fmt.Errorf("unknown fail point %q", s)
	}
}

// Config carries run-wide settings.
type Config struct {
	WorkDir       string
	Seed          int64
	Faults        FaultSet
	FailAfter     FailPoint
	SegmentTarget timeline.Ticks
	Tolerance     timeline.Ticks
}

// DefaultConfig returns production defaults for a workspace.
func DefaultConfig(workDir string, seed int64) Config {
	return Config{
		WorkDir:       workDir,
		Seed:          seed,
		SegmentTarget: timeline.MustMs(160),
		Tolerance:     timeline.MustMs(40),
	}
}

// Outcome is the result of a run (or a stopped-then-recovered run).
type Outcome struct {
	Plans       []*segment.Plan
	Alignment   *segment.Alignment
	Manifest    *manifest.Manifest
	Report      *report.Report
	Recover     *journal.Report
	ExitCode    int
	StoppedAt   FailPoint
	InputHashes map[string]string
}

// Stage directory names.
const (
	dirStreams   = "streams"
	dirIndexes   = "indexes"
	dirSegments  = "segments"
	dirQuarantine = "quarantine"
	dirManifest  = "manifest"
	dirReport    = "report"
)

// Run executes the full pipeline end to end. Recoverable input damage
// yields exit code 2; a fail-point injection yields code 1 with staged
// (not committed) work that Recover can roll back.
func Run(cfg Config) (*Outcome, error) {
	if err := os.MkdirAll(cfg.WorkDir, 0o755); err != nil {
		return nil, err
	}
	// Any previous crash is resolved before new work begins.
	rec, err := journal.Recover(cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	w, err := journal.Open(cfg.WorkDir)
	if err != nil {
		return nil, err
	}
	defer w.Close()

	specs := DefaultRepresentations()
	params := DefaultSampleParams(cfg.Seed)
	assets, err := Generate(specs, params)
	if err != nil {
		return nil, err
	}
	counts := Inject(assets, cfg.Faults, params)
	if err := writeInputs(cfg.WorkDir, assets, w); err != nil {
		return nil, err
	}

	rb := report.NewBuilder()
	var plans []*segment.Plan
	inputHashes := map[string]string{}
	hitRows, hitTotal := 0, 0
	for _, a := range assets {
		hash := SHA256Hex(a.Data)
		inputHashes[a.Spec.ID] = hash
		if _, err := w.Begin(string(timeline.StageScan), hash,
			map[string]string{"rep": a.Spec.ID}); err != nil {
			return nil, err
		}
		scan, err := bitstream.Scan(a.Spec.Codec, a.Data, bitstream.DefaultLimits())
		if err != nil {
			return nil, err
		}
		for _, e := range scan.Errors {
			rb.AddError(e)
		}
		if cfg.FailAfter == FailAfterScan {
			return stopAt(cfg, w, rb, rec, FailAfterScan)
		}
		idxFile := &index.File{Stream: a.Spec.ID, Entries: a.Entries}
		recRes := index.Reconcile(idxFile, scan)
		for _, e := range recRes.Errors {
			rb.AddError(e)
		}
		rb.QuarantineIndex(a.Spec.ID, recRes)
		hitRows += recRes.ValidHits
		hitTotal += recRes.IndexRows
		plan, err := segment.Build(a.Spec.ID, scan, recRes,
			segment.Config{TargetDuration: cfg.SegmentTarget})
		if err != nil {
			return nil, err
		}
		rb.QuarantineSegments(plan)
		plans = append(plans, plan)
	}
	segment.SortedPlans(plans)
	if cfg.FailAfter == FailAfterPlan {
		return stopAt(cfg, w, rb, rec, FailAfterPlan)
	}
	alignment, err := segment.Align(plans, segment.AlignConfig{Tolerance: cfg.Tolerance})
	if err != nil {
		rb.AddError(asTimeline(err))
		return finalizeRejected(cfg, w, rb, plans, rec, counts, inputHashes, hitRows, hitTotal)
	}
	if cfg.FailAfter == FailBeforeCommit {
		// Stage temp files but never commit: recovery must roll them back.
		if err := stageOnly(cfg.WorkDir, assets, plans, w); err != nil {
			return nil, err
		}
		return stopAt(cfg, w, rb, rec, FailBeforeCommit)
	}
	return commitAndReport(cfg, w, rb, rec, assets, plans, alignment, counts, inputHashes, hitRows, hitTotal)
}
