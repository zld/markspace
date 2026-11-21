// Package report turns checkpoint state into the terminal JSON summary and
// reads/writes baseline files bound to input fingerprint and parameter digest.
package report

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"bitrack/internal/checkpoint"
	"bitrack/internal/model"
	"bitrack/internal/quality"
)

// BaselineFileName is the default baseline written by the baseline subcommand.
const BaselineFileName = "baseline.json"

// BuildSummary converts checkpoint segments into the user-facing summary.
// Per-segment PSNR stddev and regression counts are computed here so the
// numbers in the report cannot drift from the persisted segment records.
func BuildSummary(cp *model.Checkpoint, mode string, reused int) model.Summary {
	sum := model.Summary{
		JobID:          cp.JobID,
		Mode:           mode,
		TargetKbps:     cp.Params.TargetKbps,
		MaxPSNRDropDB:  cp.Params.MaxPSNRDropDB,
		ReusedSegments: reused,
		TotalFrames:    cp.Probe.NbFrames,
		DurationSec:    cp.Probe.DurationSec,
		InputSHA256:    cp.InputSHA256,
		ParamsDigest:   cp.ParamsDigest,
		InterruptKind:  cp.InterruptKind,
		Segments:       make([]model.SegmentReport, 0, len(cp.Segments)),
	}
	var targetBits, actualBits int64
	psnrVals := make([]float64, 0, len(cp.Segments))
	for _, seg := range cp.Segments {
		targetBits += seg.TargetBits
		actualBits += seg.ActualBits
		if seg.State == model.StateCommitted {
			psnrVals = append(psnrVals, seg.PSNRDB)
		}
		sum.Segments = append(sum.Segments, model.SegmentReport{
			Index:        seg.Index,
			StartFrame:   seg.StartFrame,
			FrameCount:   seg.FrameCount,
			TargetBits:   seg.TargetBits,
			ActualBits:   seg.ActualBits,
			PSNRDB:       seg.PSNRDB,
			BaselinePSNR: seg.BaselinePSNR,
			DeltaDB:      seg.DeltaDB,
			State:        seg.State,
			Attempts:     seg.Attempts,
			Reused:       wasReused(seg),
		})
	}
	if cp.Probe.DurationSec > 0 && actualBits > 0 {
		sum.ActualKbps = float64(actualBits) / cp.Probe.DurationSec / 1000.0
		sum.BitrateDeviation = float64(actualBits-targetBits) / float64(targetBits) * 100.0
	}
	mean, stddev, regressions := quality.Aggregate(cp.Segments, cp.Params.MaxPSNRDropDB)
	sum.PSNRMeanDB, sum.SegmentPSNRStdDevDB, sum.RegressionCount = mean, stddev, regressions
	return sum
}

func wasReused(seg *model.Segment) bool {
	for _, a := range seg.Attempts {
		if a.Reason == "reused-from-checkpoint" {
			return true
		}
	}
	return false
}

// WriteJSON writes the summary as indented JSON to path atomically.
func WriteJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal report: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write report %s: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("finalize report: %w", err)
	}
	return nil
}

// SaveBaseline writes a baseline record derived from a completed run. The
// digest is the bitrate-independent encoding fingerprint, so a later run at a
// different target kbps with the same settings still matches.
func SaveBaseline(workDir string, cp *model.Checkpoint) (string, error) {
	sum := BuildSummary(cp, "baseline", 0)
	rec := model.BaselineRecord{
		InputSHA256:  cp.InputSHA256,
		ParamsDigest: cp.BaselineDigest,
		TargetKbps:   cp.Params.TargetKbps,
		Probe:        cp.Probe,
		Segments:     sum.Segments,
		PSNRMeanDB:   sum.PSNRMeanDB,
	}
	path := filepath.Join(workDir, BaselineFileName)
	if err := WriteJSON(path, rec); err != nil {
		return "", err
	}
	return path, nil
}

// LoadBaseline reads and validates a baseline file. A tampered or
// fingerprint-mismatched baseline is rejected with a locatable error.
func LoadBaseline(path, inputSHA, paramsDigest string) (*model.BaselineRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("baseline: %w", err)
	}
	var rec model.BaselineRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("baseline %s is corrupt (invalid JSON): %w", path, err)
	}
	if rec.InputSHA256 == "" || rec.ParamsDigest == "" {
		return nil, fmt.Errorf("baseline %s is corrupt: missing fingerprint fields", path)
	}
	if rec.InputSHA256 != inputSHA {
		return nil, fmt.Errorf("baseline rejected: input fingerprint mismatch (baseline=%s current=%s)",
			rec.InputSHA256, inputSHA)
	}
	if rec.ParamsDigest != paramsDigest {
		return nil, fmt.Errorf("baseline rejected: parameter digest mismatch; re-baseline after changing encode parameters")
	}
	if len(rec.Segments) == 0 {
		return nil, fmt.Errorf("baseline %s is corrupt: no segment records", path)
	}
	// Guard against an externally truncated baseline file: every segment must
	// carry plausible quality numbers.
	for i, s := range rec.Segments {
		if s.FrameCount <= 0 || s.PSNRDB <= 0 {
			return nil, fmt.Errorf("baseline %s is corrupt: segment %d has invalid records", path, i)
		}
	}
	return &rec, nil
}

// CorruptBaselineForTesting overwrites the baseline with invalid JSON. It
// exists for the recovery/injection tests and is not used on the happy path.
func CorruptBaselineForTesting(path string) error {
	return os.WriteFile(path, []byte("{not-valid-json"), 0o644)
}

// EnsureBaselineDigest hashes the bitrate-independent parameter subset.
func EnsureBaselineDigest(p model.Params) (string, error) {
	return checkpoint.DigestBaseline(p)
}
