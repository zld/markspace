package main

import (
	"path/filepath"

	"mediaforensics/internal/fixture"
	"mediaforensics/internal/manifest"
	"mediaforensics/internal/model"
	"mediaforensics/internal/parse"
	"mediaforensics/internal/render"
	"mediaforensics/internal/repair"
	"mediaforensics/internal/rtp"
	"mediaforensics/internal/validate"
)

// runRTPWindowCase exercises the unrecoverable transport path explicitly.
func runRTPWindowCase(root string, seed int64, frames int) (caseResult, error) {
	dir := filepath.Join(root, fixture.CaseRTPWindow)
	w, err := manifest.NewWriter(dir)
	if err != nil {
		return caseResult{}, err
	}
	cfg := fixture.DefaultRTPConfig(seed, frames*4, 0.0)
	// A fixed 6-packet window gap stays unrecoverable while keeping the
	// aggregate recovered rate above the 0.90 threshold even for short runs.
	stream := fixture.GenerateRTPWindowLoss(cfg, 6)
	wire, err := fixture.MarshalPackets(stream)
	if err != nil {
		return caseResult{}, err
	}
	rec := rtp.NewRecoverer(rtp.DefaultWindow, cfg.FrameStep, cfg.ClockRate)
	for _, raw := range wire {
		_ = rec.Ingest(raw)
	}
	stats, errs := rec.Close()
	stage := manifest.StageRecord{
		Name: manifest.StageValidate, Status: "degraded",
		Errors: errs, Detail: "window loss unrecoverable",
	}
	if _, err := w.WriteStage(stage); err != nil {
		return caseResult{}, err
	}
	exit := 0
	// Unrecoverable packets mean damage handled per policy: full evidence but
	// exit 2 even when the aggregate rate stays above threshold.
	if stats.Unrecoverable > 0 || len(errs) > 0 {
		exit = 2
	}
	if stats.RecoveredRate() < validate.DefaultThresholds().RecoveredRate ||
		stats.P95LatencyMs > validate.DefaultThresholds().P95LatencyMs {
		exit = 1
	}
	rm := manifest.RunManifest{
		Seed: seed, Case: fixture.CaseRTPWindow, Stages: []manifest.StageRecord{stage},
		RecoveredRate: stats.RecoveredRate(), P95LatencyMs: stats.P95LatencyMs,
		ErrorSummary: manifest.ErrorDigest(errs), ExitCode: exit,
	}
	_, sha, err := w.WriteRun(rm)
	if err != nil {
		return caseResult{}, err
	}
	return caseResult{name: fixture.CaseRTPWindow, offset: -1,
		recoveredRate: stats.RecoveredRate(), p95LatencyMs: stats.P95LatencyMs,
		exitCode: exit, manifestSHA: sha}, nil
}

// finishPipeline runs repair, render and validate for a parsed container and
// writes the per-stage evidence plus the run manifest.
func finishPipeline(w *manifest.Writer, res *parse.Result, name string, dmgOffset int64,
	seed int64, frames int, stats rtp.Stats) (caseResult, error) {
	rep := repair.Segment(res.Timeline)
	transform := manifest.StageRecord{
		Name: manifest.StageTransform, Status: statusFor(len(rep.Errors)),
		Errors: rep.Errors, Detail: "repair segmentation and timeline normalization",
	}
	if _, err := w.WriteStage(transform); err != nil {
		return caseResult{}, err
	}
	renderer, rerrs := render.NewRenderer(res.Timeline, render.Config{
		OutputDir: filepath.Join(w.Dir(), "frames"), FrameCount: frames,
		FrameRateNum: 24000, FrameRateDen: 1001, TransitionEvery: 8,
	})
	fm, err := renderer.Render(frames)
	if err != nil {
		return caseResult{}, err
	}
	renderStage := manifest.StageRecord{
		Name: manifest.StageRender, Status: statusFor(len(rerrs) + fm.GapCount),
		Errors: rerrs, Artifacts: []string{"frames/frames.manifest.json"},
	}
	if _, err := w.WriteStage(renderStage); err != nil {
		return caseResult{}, err
	}
	report := validate.Check(res.Timeline, fm, rep, stats, validate.DefaultThresholds())
	validateStage := manifest.StageRecord{
		Name: manifest.StageValidate, Status: statusFor(len(report.Errors)),
		Errors: report.Errors,
	}
	if _, err := w.WriteStage(validateStage); err != nil {
		return caseResult{}, err
	}
	allErrs := append(append(append([]*model.ForensicError{}, res.Errors...), rep.Errors...), report.Errors...)
	exit := 0
	switch {
	case hasHardFailure(report):
		exit = 1
	case len(allErrs) > 0 || fm.GapCount > 0:
		exit = 2
	}
	rm := manifest.RunManifest{
		Seed: seed, Case: name,
		Stages:     []manifest.StageRecord{transform, renderStage, validateStage},
		FrameCount: fm.FrameCount, GapCount: fm.GapCount,
		SyncErrorMs: report.MaxSyncErrorMs, RecoveredRate: report.RecoveredRate,
		P95LatencyMs: report.P95LatencyMs, ErrorSummary: manifest.ErrorDigest(allErrs),
		ExitCode: exit,
	}
	_, sha, err := w.WriteRun(rm)
	if err != nil {
		return caseResult{}, err
	}
	return caseResult{
		name: name, offset: dmgOffset, recoveredRate: report.RecoveredRate,
		syncErrorMs: report.MaxSyncErrorMs, p95LatencyMs: report.P95LatencyMs,
		exitCode: exit, manifestSHA: sha,
	}, nil
}
