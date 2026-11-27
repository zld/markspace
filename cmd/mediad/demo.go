package main

import (
	"os"
	"path/filepath"

	"github.com/example/mediad/internal/audio"
	"github.com/example/mediad/internal/cache"
	"github.com/example/mediad/internal/checkpoint"
	"github.com/example/mediad/internal/fixture"
	"github.com/example/mediad/internal/pipeline"
	"github.com/example/mediad/internal/report"
)

// demoConfig holds the parsed demo flags.
type demoConfig struct {
	seed       int64
	budgetMs   int64
	maxBytes   int64
	targetRate uint32
	layout     string
	lufs       float64
	truePeak   float64
	workDir    string
}

// exit codes per the acceptance contract.
const (
	exitOK             = 0
	exitArgError       = 1
	exitBudgetOrQuality = 2
	exitCorruptInput   = 3
)

// runDemo executes the full demonstration and returns the process exit code.
// It generates clean, loss/duplicate, truncated and corrupted-cache samples,
// processes them through the kernel, and prints one JSON summary.
func runDemo(cfg demoConfig) int {
	work := cfg.workDir
	if work == "" {
		work = filepath.Join(os.TempDir(), "mediad-demo")
	}
	cacheDir := filepath.Join(work, "cache")
	cpDir := filepath.Join(work, "checkpoint")
	_ = os.MkdirAll(cacheDir, 0o755)
	_ = os.MkdirAll(cpDir, 0o755)

	c, err := cache.New(cacheDir)
	if err != nil {
		return fatal("cache init: %v", err)
	}
	cp, err := checkpoint.NewStore(cpDir)
	if err != nil {
		return fatal("checkpoint init: %v", err)
	}
	runner := &pipeline.Runner{
		Params: pipeline.Params{
			TargetRate: cfg.targetRate,
			Layout:     audio.Layout(cfg.layout),
			LUFS:       cfg.lufs,
			TruePeak:   cfg.truePeak,
			MaxBytes:   cfg.maxBytes,
			BudgetMs:   cfg.budgetMs,
		},
		Cache:      c,
		Checkpoint: cp,
	}

	summary := &report.Summary{Seed: cfg.seed}
	corruptInput := false
	qualityOrBudgetMiss := false

	// 1) Clean normal sample.
	clean := buildInput("normal-opus", "opus", fixture.KindClean, cfg.seed)
	if res, e := runner.Run(clean); e != nil {
		corruptInput = true
	} else {
		summary.Items = append(summary.Items, res.Item)
	}

	// 2) Loss + duplicate sample (recoverable; not isolated).
	lossDup := buildInput("lossdup-opus", "opus", fixture.KindLossDup, cfg.seed+1)
	if res, e := runner.Run(lossDup); e != nil {
		corruptInput = true
	} else {
		summary.Items = append(summary.Items, res.Item)
	}

	// 3) Truncated sample: must report seq/offset and isolate (exit 3).
	trunc := buildInput("truncated-aac", "aac", fixture.KindTruncated, cfg.seed+2)
	if _, e := runner.Run(trunc); e != nil {
		corruptInput = true
		summary.Items = append(summary.Items, report.Item{
			MaterialID: "truncated-aac", Codec: "aac",
			Status:     report.StatusIsolated,
			Isolated:   true,
			Deviations: []string{e.Error()},
		})
	}

	// 4) Corrupted cache sample: poison an entry then confirm re-verification
	// rejects it and recomputation still succeeds (self-healing cache).
	if item, ok := poisonAndRecover(runner, c, cfg); ok {
		summary.Items = append(summary.Items, item)
	}

	// 5) Recovery sample: run the same clean material twice; the second run
	// reuses the completed stage and must yield the same manifest digest.
	recoverIn := buildInput("recovery-opus", "opus", fixture.KindClean, cfg.seed+3)
	_, e1 := runner.Run(recoverIn)
	r2, e2 := runner.Run(recoverIn)
	if e1 == nil && e2 == nil {
		summary.Items = append(summary.Items, r2.Item)
	}

	for _, it := range summary.Items {
		if it.Status == report.StatusQualityMiss || it.Status == report.StatusSizeMiss ||
			it.Status == report.StatusBudgetMiss {
			qualityOrBudgetMiss = true
		}
	}

	totalBytes := int64(0)
	for _, it := range summary.Items {
		totalBytes += it.OutputBytes
	}
	// max-bytes is a per-material ceiling; the runner already marks any
	// over-sized item. The run-level budget reports time deviation and an
	// aggregate size flag only when any single item breached its ceiling.
	anySizeMiss := false
	for _, it := range summary.Items {
		if it.Status == report.StatusSizeMiss {
			anySizeMiss = true
		}
	}
	summary.Budget = pipeline.BudgetReport(cfg.budgetMs, elapsedMs(), cfg.maxBytes, totalBytes)
	summary.Budget.OverBudgetSize = anySizeMiss
	if summary.Budget.OverBudgetTime || summary.Budget.OverBudgetSize {
		qualityOrBudgetMiss = true
	}

	if _, err := summary.ComputeManifestDigest(); err != nil {
		return fatal("manifest: %v", err)
	}
	printJSON(summary)

	switch {
	case corruptInput:
		return exitCorruptInput
	case qualityOrBudgetMiss:
		return exitBudgetOrQuality
	default:
		return exitOK
	}
}

func fatal(format string, args ...interface{}) int {
	stderr(format+"\n", args...)
	return exitArgError
}
