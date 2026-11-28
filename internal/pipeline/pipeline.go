// Package pipeline wires the subsystems together for the CLI and the
// end-to-end acceptance tests. It owns the sequencing: fixture generation,
// manifest verification, RTP reorder, timeline merge, planning, offscreen
// export and the independent verification pass.
package pipeline

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"medialab/internal/artifact"
	"medialab/internal/fixtures"
	"medialab/internal/ingest"
	"medialab/internal/manifestio"
	"medialab/internal/model"
	"medialab/internal/planner"
	"medialab/internal/render"
	"medialab/internal/timeline"
	"medialab/internal/verify"
)

// Config is one demo invocation.
type Config struct {
	Case string
	Seed int64
	// Budgets.
	BudgetMS   int64
	MaxBytes   int64
	Workers    int
	CacheBytes int64
	ReorderWin int
	MaxDim     int
	MinQuality float64
	// AlignToleranceNS is the largest per-event timing deviation accepted,
	// both when aligning the merged timeline and when the verifier re-checks
	// the exported segments.
	AlignToleranceNS int64
	// OutDir receives the generated input and exported artefacts. Empty means
	// a fresh directory under os.TempDir.
	OutDir string
	// KeepInput preserves the input fixture directory; otherwise it is removed
	// when the run ends. Artefacts always remain under OutDir/out.
	KeepInput bool
}

// Defaults fills unset configuration.
func Defaults(c Config) Config {
	if c.Case == "" {
		c.Case = fixtures.CaseNormal
	}
	if c.BudgetMS <= 0 {
		c.BudgetMS = 500
	}
	if c.MaxBytes <= 0 {
		c.MaxBytes = 1 << 20
	}
	if c.Workers <= 0 {
		c.Workers = 2
	}
	if c.CacheBytes <= 0 {
		c.CacheBytes = 24 << 20
	}
	if c.ReorderWin <= 0 {
		c.ReorderWin = 8
	}
	if c.MaxDim <= 0 {
		c.MaxDim = model.MaxDimension
	}
	if c.MinQuality <= 0 {
		c.MinQuality = 0.5
	}
	if c.AlignToleranceNS <= 0 {
		c.AlignToleranceNS = 2_000_000
	}
	return c
}

// Result is the complete outcome of a demo run.
type Result struct {
	Config       Config
	InputDir     string
	OutDir       string
	ManifestPath string
	Dataset      *fixtures.Dataset
	Plan         *planner.Plan
	Timeline     *timeline.Timeline
	Align        timeline.AlignReport
	Render       *render.Result
	Verify       *verify.Report
	Faults       model.Faults
	Warnings     model.Faults
	Elapsed      time.Duration
	// ExitCode follows the CLI contract: 0 success or recoverable-only,
	// 1 input hard error, 2 verification failure.
	ExitCode int
}

// Run executes the whole pipeline. A non-nil error is reserved for programmer
// or environment failures (I/O outside artefacts); input and verification
// problems are reported through Result.Faults and Result.ExitCode.
func Run(ctx context.Context, cfg Config) (*Result, error) {
	cfg = Defaults(cfg)
	started := time.Now()
	res := &Result{Config: cfg}
	if cfg.OutDir == "" {
		dir, err := os.MkdirTemp("", "medialab-"+cfg.Case+"-")
		if err != nil {
			return nil, err
		}
		cfg.OutDir = dir
		res.Config = cfg
	}
	res.InputDir = filepath.Join(cfg.OutDir, "input")
	res.OutDir = filepath.Join(cfg.OutDir, "out")

	ds, err := fixtures.Generate(fixtures.DefaultSpec(cfg.Case, cfg.Seed, res.InputDir))
	if err != nil {
		return nil, fmt.Errorf("generate fixture: %w", err)
	}
	res.Dataset = ds

	// Stage 1: manifest validation (content hashes included).
	input, warns, loadErr := ingest.Load(ds.Manifest, ingest.LoadOptions{
		RootDir: res.InputDir, VerifyContent: true,
	})
	if loadErr != nil {
		res.Faults = model.Faults{faultOf(loadErr)}
		res.ExitCode = model.ExitCode(res.Faults)
		res.Elapsed = time.Since(started)
		return res, nil
	}
	res.Warnings = append(res.Warnings, warns...)

	// Stage 2: RTP reorder and assembly, then timeline merge. The assembled
	// payloads are authoritative for render: frames whose fragments did not all
	// arrive become deterministic fill frames.
	assemblyWarns, assembled := reorderAll(res.InputDir, ds, cfg.ReorderWin)
	res.Warnings = append(res.Warnings, assemblyWarns...)

	tl, mergeWarns, mergeErr := timeline.Merge(input.Order, timeline.MergeOptions{})
	if mergeErr != nil {
		res.Faults = append(res.Faults, faultOf(mergeErr))
		res.ExitCode = model.ExitCode(res.Faults)
		res.Elapsed = time.Since(started)
		return res, nil
	}
	res.Warnings = append(res.Warnings, mergeWarns...)
	res.Timeline = tl

	// Stage 3: alignment against the shared clock.
	align, err := timeline.Align(tl, timeline.AlignConfig{MaxErrorNS: cfg.AlignToleranceNS})
	if err != nil {
		return nil, err
	}
	res.Align = align
	res.Warnings = append(res.Warnings, align.Violations.Recoverable()...)

	// Stage 4: planning under the budgets.
	plan, planErr := planner.Choose(plannerInput(res.InputDir, ds, cfg.MinQuality), plannerBudget(cfg))
	res.Plan = plan
	if planErr != nil {
		res.Faults = append(res.Faults, planFault(plan, planErr))
		res.ExitCode = 1
		res.Elapsed = time.Since(started)
		return res, nil
	}

	// Stage 5: offscreen export.
	params := &artifact.Params{
		Quality: plan.Chosen.Quality, QuantStep: plan.Chosen.QuantStep,
		FPS: plan.Chosen.FPS, FillPolicy: render.FillPolicy,
		Workers: cfg.Workers, MaxCacheBytes: cfg.CacheBytes,
		ReorderWindow: cfg.ReorderWin, MaxDimension: cfg.MaxDim,
		MaxBudgetMS: cfg.BudgetMS, MaxBytes: cfg.MaxBytes,
	}
	rendered, err := render.Export(ctx, tl, render.Options{
		OutDir: res.OutDir, Plan: params, Workers: cfg.Workers,
		CacheCap: cfg.CacheBytes, Fill: render.FillPolicy,
		Keep:   frameSampler(plan.Chosen.FPS),
		Decode: makeDecoder(res.InputDir, ds, assembled),
	})
	if err != nil {
		res.Faults = append(res.Faults, faultOf(err))
		res.ExitCode = model.ExitCode(res.Faults)
		res.Elapsed = time.Since(started)
		return res, nil
	}
	rendered.Manifest.Summary.PeakCacheBytes = rendered.PeakCacheBytes
	res.Render = rendered
	res.Warnings = append(res.Warnings, rendered.Warnings...)
	res.Faults = append(res.Faults, rendered.Faults...)

	// Stage 6: write the manifest and independently verify it from disk.
	res.ManifestPath = filepath.Join(res.OutDir, "manifest.json")
	if err := manifestio.Write(res.ManifestPath, &rendered.Manifest); err != nil {
		return nil, err
	}
	rep, err := verify.Verify(res.OutDir, verify.Config{
		MaxDimension: cfg.MaxDim, MaxBytes: cfg.MaxBytes,
		MaxAlignErrorNS: cfg.AlignToleranceNS, MinLoudnessDBFS: -100,
	})
	if err != nil {
		return nil, err
	}
	res.Verify = rep
	res.Faults = append(res.Faults, rep.Faults...)
	res.Faults = res.Faults.Dedupe()
	res.Warnings = res.Warnings.Dedupe()
	res.Faults.Sort()
	res.Warnings.Sort()
	res.ExitCode = model.ExitCode(res.Faults)
	res.Elapsed = time.Since(started)

	if !cfg.KeepInput {
		// Artefacts stay; the input fixture is disposable.
		_ = os.RemoveAll(res.InputDir)
	}
	return res, nil
}
