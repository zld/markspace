// Package batch orchestrates offline scanning, policy, repacking and audit recovery.
package batch

import (
	"errors"
	"math"

	"mediaflow/internal/audit"
	"mediaflow/internal/fault"
	"mediaflow/internal/index"
	"mediaflow/internal/plan"
	"mediaflow/internal/policy"
	"mediaflow/internal/probe"
	"mediaflow/internal/repack"
	"mediaflow/internal/safepath"
)

// Hooks allows fault injection at repack chunk boundaries.
type Hooks struct {
	AfterFirstSample func(itemID string, chunkIndex int, tempPath string) error
}

// Options controls a batch run.
type Options struct {
	WorkDir  string
	BatchKey string
	Hooks    Hooks
}

// RunManifest processes every manifest item and never stops at the first failure.
func RunManifest(m *probe.Manifest, opt Options) *audit.Summary {
	summary := &audit.Summary{BatchKey: batchKey(m, opt.BatchKey)}
	validation := m.Validate()
	invalidByID := map[string][]*fault.Error{}
	for _, fe := range validation {
		if fe.ItemID != "" {
			invalidByID[fe.ItemID] = append(invalidByID[fe.ItemID], fe)
		}
	}
	for _, fe := range validation {
		if fe.ItemID == "" {
			summary.Add(&audit.ItemReport{
				ItemID: "<manifest>", Status: audit.StatusManifestError,
				Reasons: []string{fe.Error()},
			})
			summary.ExitCode = 2
			return summary
		}
	}

	duplicateIDs := map[string]int{}
	for _, it := range m.Items {
		duplicateIDs[it.ID]++
	}

	for i := range m.Items {
		it := m.Items[i]
		itemBatchKey := m.ItemBatchKey(it)
		if opt.BatchKey != "" {
			itemBatchKey = opt.BatchKey
		}
		report := processItem(it, m, opt, itemBatchKey, invalidByID[it.ID], duplicateIDs[it.ID] > 1)
		summary.Add(report)
	}
	summary.ExitCode = summary.RecomputeExit()
	return summary
}

func batchKey(m *probe.Manifest, override string) string {
	if override != "" {
		return override
	}
	return m.BatchKey
}

func processItem(it probe.Item, m *probe.Manifest, opt Options, batchKey string, validation []*fault.Error, duplicate bool) *audit.ItemReport {
	rep := &audit.ItemReport{ItemID: it.ID, BudgetMbps: m.BudgetMbps, QualityMin: m.QualityMin}
	if it.BudgetMbps > 0 {
		rep.BudgetMbps = it.BudgetMbps
	}
	if it.QualityMin > 0 {
		rep.QualityMin = it.QualityMin
	}
	if len(validation) > 0 || duplicate {
		rep.Status = audit.StatusManifestError
		for _, fe := range validation {
			rep.Reasons = append(rep.Reasons, fe.Error())
		}
		if duplicate {
			rep.Reasons = append(rep.Reasons, "duplicate item id must not be processed ambiguously")
		}
		return rep
	}

	inputPath, err := safepath.Resolve(opt.WorkDir, it.Path)
	if err != nil {
		rep.Status = audit.StatusManifestError
		rep.Reasons = append(rep.Reasons, err.Error())
		return rep
	}
	if checked, err := safepath.CheckExistingWithin(opt.WorkDir, inputPath); err != nil {
		rep.Status = audit.StatusManifestError
		rep.Reasons = append(rep.Reasons, err.Error())
		return rep
	} else {
		inputPath = checked
	}
	scan, err := probe.ScanFile(inputPath)
	if err != nil {
		rep.Status = audit.StatusIOError
		rep.Reasons = append(rep.Reasons, err.Error())
		if fe, ok := fault.AsError(err); ok {
			rep.Reasons = append(rep.Reasons, string(fe.Class))
		}
		return rep
	}
	rep.InputDigest = scan.InputDigest
	rep.Intervals = append(rep.Intervals, scan.Intervals...)
	rep.Intervals = append(rep.Intervals, probe.ValidateExpectations(it, scan)...)
	rep.AnomalyCount = len(rep.Intervals)

	if hasRejectInterval(rep.Intervals) {
		rep.Status = audit.StatusRejected
		rep.Reasons = append(rep.Reasons, "scan or expectation validation contains a rejecting interval")
		return rep
	}

	target := makeTarget(it, m, scan)
	cfg, err := plan.New(scan.InputDigest, target, scan.Stats, chunkSamples(it, m))
	if err != nil {
		rep.Status = audit.StatusPolicyFailed
		rep.Reasons = append(rep.Reasons, err.Error())
		return rep
	}
	rep.Config = cfg
	rep.ConfigDigest = cfg.Digest
	rep.QualityScore = cfg.Decision.QualityScore
	rep.QualityPass = cfg.Decision.QualityPass
	rep.TargetBitrateKbps = cfg.Decision.TargetTotalKbps
	rep.Tolerance = target.Tolerance
	rep.Reasons = append(rep.Reasons, cfg.Decision.Reasons...)

	if !cfg.Decision.Feasible {
		fillPolicyFailure(rep, cfg)
		return rep
	}

	hooks := repack.Hooks{}
	if opt.Hooks.AfterFirstSample != nil {
		hooks.AfterFirstSample = func(chunkIndex int, tempPath string) error {
			return opt.Hooks.AfterFirstSample(it.ID, chunkIndex, tempPath)
		}
	}
	out, err := repack.Process(repack.Options{
		WorkDir: opt.WorkDir, BatchKey: batchKey, ItemID: it.ID,
		InputPath: inputPath, Report: scan, Config: cfg, Hooks: hooks,
	})
	if err != nil {
		var compat *index.CompatibilityError
		if errors.As(err, &compat) {
			rep.Status = audit.StatusCheckpointConflict
			rep.Reasons = append(rep.Reasons, compat.Reasons...)
			return rep
		}
		rep.Status = audit.StatusChunkFailed
		rep.Reasons = append(rep.Reasons, "chunk processing failed: "+err.Error())
		return rep
	}

	rep.OutputDigest = out.OutputDigest
	rep.OutputPath = out.OutputPath
	rep.CheckpointPath = out.CheckpointPath
	rep.ActualBitrateKbps = out.ActualKbps
	rep.ActualMbps = float64(out.ActualKbps) / 1000.0
	rep.BudgetDeviationMbps = round3(float64(out.ActualKbps)/1000.0 - target.BudgetMbps)
	rep.ChunkCount = out.PlannedChunks
	rep.WrittenChunks = out.WrittenChunks
	rep.SkippedChunks = out.SkippedChunks
	rep.RolledBackChunks = out.RolledBack
	rep.CompletedChunks = out.PlannedChunks // Process returns only after all chunks assemble.
	rep.BitrateWithinTol = policy.WithinBudgetTolerance(cfg.Decision.TargetTotalKbps, out.ActualKbps, target.Tolerance)
	if !rep.BitrateWithinTol {
		rep.Status = audit.StatusBudgetFailed
		rep.BudgetDeviationMbps = round3(float64(out.ActualKbps-cfg.Decision.TargetTotalKbps) / 1000.0)
		rep.Reasons = append(rep.Reasons, "actual bitrate differs from configured target beyond tolerance")
		return rep
	}
	rep.Status = audit.StatusPass
	return rep
}

func makeTarget(it probe.Item, m *probe.Manifest, r *probe.Report) policy.Target {
	budget := m.BudgetMbps
	if it.BudgetMbps > 0 {
		budget = it.BudgetMbps
	}
	q := m.QualityMin
	if it.QualityMin > 0 {
		q = it.QualityMin
	}
	w, h, fps := it.Width, it.Height, it.FPS
	if v := primaryVideo(r); v != nil {
		if w == 0 {
			w = int(v.Width)
		}
		if h == 0 {
			h = int(v.Height)
		}
	}
	if fps == 0 {
		fps = r.Stats.FPS
	}
	if w == 0 {
		w = 1920
	}
	if h == 0 {
		h = 1080
	}
	if fps == 0 {
		fps = 30
	}
	return policy.Target{
		Width: w, Height: h, FPS: fps, QualityMin: q,
		BudgetMbps: budget, AudioKbps: 192, Tolerance: 0.05,
	}
}

func primaryVideo(r *probe.Report) *probe.Track {
	for _, t := range r.Tracks {
		if t != nil && t.Type == "video" {
			return t
		}
	}
	return nil
}

func chunkSamples(it probe.Item, m *probe.Manifest) int {
	if it.ChunkSamples > 0 {
		return it.ChunkSamples
	}
	if m.ChunkSamples > 0 {
		return m.ChunkSamples
	}
	return 64
}

func hasRejectInterval(in []fault.Interval) bool {
	for _, x := range in {
		if x.Reject {
			return true
		}
	}
	return false
}

func fillPolicyFailure(rep *audit.ItemReport, cfg *plan.Config) {
	rep.TargetBitrateKbps = cfg.Decision.TargetTotalKbps
	rep.BudgetDeviationMbps = cfg.Decision.BudgetDeviationMbps
	hasBudget, hasQuality := false, false
	for _, d := range cfg.Decision.Deviations {
		if d.Constraint == "bitrate_budget" {
			hasBudget = true
		}
		if d.Constraint == "quality_min" {
			hasQuality = true
		}
	}
	switch {
	case hasBudget:
		rep.Status = audit.StatusBudgetFailed
	case hasQuality:
		rep.Status = audit.StatusQualityFailed
	default:
		rep.Status = audit.StatusPolicyFailed
	}
}

func round3(v float64) float64 {
	return math.Round(v*1000) / 1000
}
