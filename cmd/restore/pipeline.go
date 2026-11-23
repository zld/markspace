package main

import (
	"fmt"
	"os"
	"path/filepath"

	"mediaforensics/internal/fixture"
	"mediaforensics/internal/manifest"
	"mediaforensics/internal/model"
	"mediaforensics/internal/parse"
	"mediaforensics/internal/render"
)

// runAllCases executes normal, truncated, corrupt, missing-index, window-loss
// and clock-overflow paths; the checksum is the primary run digest.
func runAllCases(root string, seed int64, frames int, loss float64) ([]caseResult, string, error) {
	if frames <= 0 || frames > render.MaxFrames {
		return nil, "", fmt.Errorf("frames must be within 1..%d, got %d", render.MaxFrames, frames)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, "", err
	}
	var results []caseResult
	normal, err := runContainerCase(root, fixture.CaseNormal, seed, frames, loss, nil)
	if err != nil {
		return nil, "", err
	}
	results = append(results, normal)

	trunc, err := runContainerCase(root, fixture.CaseTruncated, seed, frames, loss,
		func(data []byte, layout *fixture.Layout) ([]byte, fixture.Damage, error) {
			return fixture.TruncateBox(data, layout, 128)
		})
	if err != nil {
		return nil, "", err
	}
	results = append(results, trunc)

	corrupt, err := runContainerCase(root, fixture.CaseCorrupt, seed, frames, loss,
		func(data []byte, layout *fixture.Layout) ([]byte, fixture.Damage, error) {
			return fixture.CorruptSample(data, layout, 1, frames/4)
		})
	if err != nil {
		return nil, "", err
	}
	results = append(results, corrupt)

	missing, err := runMissingIndexCase(root, seed, frames, loss)
	if err != nil {
		return nil, "", err
	}
	results = append(results, missing)

	window, err := runRTPWindowCase(root, seed, frames)
	if err != nil {
		return nil, "", err
	}
	results = append(results, window)

	overflow, err := runClockOverflowCase(root, seed, frames)
	if err != nil {
		return nil, "", err
	}
	results = append(results, overflow)
	return results, normal.manifestSHA, nil
}

// runContainerCase drives one possibly-damaged MP4 through the full pipeline.
func runContainerCase(root, name string, seed int64, frames int, loss float64, damage damageFunc) (caseResult, error) {
	dir := filepath.Join(root, name)
	spec := fixture.DefaultSpec(seed, frames)
	data, layout, err := fixture.GenerateMP4(spec)
	if err != nil {
		return caseResult{}, err
	}
	dmg := fixture.Damage{Case: name, Offset: -1}
	parseFailed := false
	if damage != nil {
		data, dmg, err = damage(data, layout)
		if err != nil {
			return caseResult{}, err
		}
	}
	w, err := manifest.NewWriter(dir)
	if err != nil {
		return caseResult{}, err
	}

	res, perr := parse.Parse(data)
	stage := manifest.StageRecord{Name: manifest.StageParse, Status: "ok", Errors: collectErrs(res)}
	if perr != nil {
		parseFailed = true
		stage.Status = "failed"
		stage.Offset = model.AsForensicError(perr).Offset
		stage.Errors = append(stage.Errors, model.AsForensicError(perr))
	}
	if _, err := w.WriteStage(stage); err != nil {
		return caseResult{}, err
	}

	// Transport recovery runs independently of container damage.
	stats := runRTP(seed, frames, loss)
	if parseFailed {
		rm := manifest.RunManifest{
			Seed: seed, Case: name, Stages: []manifest.StageRecord{stage},
			RecoveredRate: stats.RecoveredRate(), P95LatencyMs: stats.P95LatencyMs,
			ErrorSummary: manifest.ErrorDigest(stage.Errors), ExitCode: 2,
		}
		_, sha, err := w.WriteRun(rm)
		if err != nil {
			return caseResult{}, err
		}
		return caseResult{name: name, offset: dmg.Offset, recoveredRate: stats.RecoveredRate(),
			syncErrorMs: 0, p95LatencyMs: stats.P95LatencyMs, exitCode: 2, manifestSHA: sha}, nil
	}

	return finishPipeline(w, res, name, dmg.Offset, seed, frames, stats)
}

// runMissingIndexCase regenerates a container without stco/stsc on track 1.
func runMissingIndexCase(root string, seed int64, frames int, loss float64) (caseResult, error) {
	dir := filepath.Join(root, fixture.CaseMissingIndex)
	base := fixture.DefaultSpec(seed, frames)
	spec, dmg := fixture.MissingIndex(base, 1)
	data, _, err := fixture.GenerateMP4(spec)
	if err != nil {
		return caseResult{}, err
	}
	w, err := manifest.NewWriter(dir)
	if err != nil {
		return caseResult{}, err
	}
	res, perr := parse.Parse(data)
	stage := manifest.StageRecord{Name: manifest.StageParse, Status: "ok", Errors: collectErrs(res)}
	if perr != nil {
		stage.Status = "failed"
		stage.Errors = append(stage.Errors, model.AsForensicError(perr))
	}
	if _, err := w.WriteStage(stage); err != nil {
		return caseResult{}, err
	}
	// The boundary rebuild records a forensic error with the first located
	// sample offset; surface it as the case's auditable byte offset.
	rebuiltOffset := dmg.Offset
	for _, e := range stage.Errors {
		if e.Stage == model.StageIndex && e.Offset >= 0 {
			rebuiltOffset = e.Offset
			break
		}
	}
	stats := runRTP(seed, frames, loss)
	return finishPipeline(w, res, fixture.CaseMissingIndex, rebuiltOffset, seed, frames, stats)
}

