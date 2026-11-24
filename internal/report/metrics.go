// Package report turns committed analysis records into a deterministic,
// self-describing JSON summary. Every run with the same input produces the
// same field order and the same SHA-256 hash.
package report

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
	"sort"

	"mediacheck/internal/clock"
	"mediacheck/internal/dash"
	"mediacheck/internal/model"
	"mediacheck/internal/normalize"
	"mediacheck/internal/probe"
	"mediacheck/internal/safeio"
)

// DamagedRange is one timeline interval with the faults located inside it.
type DamagedRange struct {
	Representation string            `json:"representation"`
	StartTick      int64             `json:"start_tick"`
	EndTick        int64             `json:"end_tick"`
	Reasons        []model.FaultKind `json:"reasons"`
}

// Line is the single JSON object emitted per run.
type Line struct {
	TargetBitrateBps      int64                `json:"target_bitrate_bps"`
	ActualBitrateBps      int64                `json:"actual_bitrate_bps"`
	BitrateDeviationPct   float64              `json:"bitrate_deviation_pct"`
	QualityMean           float64              `json:"quality_mean"`
	QualityP95            float64              `json:"quality_p95"`
	ManifestAlignmentMs   float64              `json:"manifest_alignment_ms"`
	DamagedRanges         []DamagedRange       `json:"damaged_ranges"`
	MetadataConflicts     []normalize.Conflict `json:"metadata_conflicts"`
	RecoveredTransactions int                  `json:"recovered_transactions"`
	ReportSHA256          string               `json:"report_sha256"`
}

// Inputs bundles everything needed to compute the summary.
type Inputs struct {
	Manifest  *dash.Manifest
	Probe     *probe.Report
	Normalize *normalize.Report
	Recovered int
	Issues    []model.Issue
}

// Build computes the summary. Aggregate bitrate is the size-weighted mean
// across representations; alignment error is measured in milliseconds against
// the manifest's planned segment starts on the master timeline.
func Build(in Inputs, lim safeio.Limits) (*Line, error) {
	line := &Line{
		DamagedRanges:         []DamagedRange{},
		MetadataConflicts:     []normalize.Conflict{},
		RecoveredTransactions: in.Recovered,
	}
	line.DamagedRanges = collectDamaged(in.Probe)
	line.MetadataConflicts = sortConflicts(in.Normalize)
	var targetSum, actualSum int64
	var quality []float64
	var alignErrTick int64
	var alignCount int64
	for _, r := range in.Manifest.Representations {
		targetSum += r.TargetBitrate
		actualSum += r.ActualBitrate
		quality = append(quality, r.QualityScores...)
		for _, s := range r.Segments {
			// Start skew captures late delivery / clock drift; end skew
			// captures truncation, where the actual media ends early.
			startDiff := s.StartTick - s.PlannedStartTick
			if startDiff < 0 {
				startDiff = -startDiff
			}
			endDiff := s.EndTick - s.PlannedEndTick
			if endDiff < 0 {
				endDiff = -endDiff
			}
			alignErrTick += startDiff + endDiff
			alignCount += 2
		}
	}
	line.TargetBitrateBps = targetSum
	line.ActualBitrateBps = actualSum
	if targetSum > 0 {
		line.BitrateDeviationPct = float64(actualSum-targetSum) / float64(targetSum) * 100
	}
	line.QualityMean = mean(quality)
	line.QualityP95 = percentile(quality, 95)
	if alignCount > 0 {
		avgTick := float64(alignErrTick) / float64(alignCount)
		line.ManifestAlignmentMs = avgTick / float64(clock.MasterTicksPerSecond) * 1000
	}
	// Deterministic serialization: hash the canonical JSON without the hash
	// field itself, then embed the digest.
	hash, err := canonicalHash(line)
	if err != nil {
		return nil, err
	}
	line.ReportSHA256 = hash
	if int64(len(canonicalBytes(line))) > lim.MaxReportBytes {
		return nil, model.ErrReportTooLarge
	}
	return line, nil
}

func collectDamaged(rep *probe.Report) []DamagedRange {
	out := []DamagedRange{}
	if rep == nil {
		return out
	}
	for _, sr := range rep.Segments {
		if !sr.Damaged {
			continue
		}
		reasons := append([]model.FaultKind(nil), sr.Reasons...)
		sort.Slice(reasons, func(i, j int) bool { return reasons[i] < reasons[j] })
		out = append(out, DamagedRange{
			Representation: sr.Representation,
			StartTick:      sr.Range.Start,
			EndTick:        sr.Range.End,
			Reasons:        reasons,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Representation != out[j].Representation {
			return out[i].Representation < out[j].Representation
		}
		return out[i].StartTick < out[j].StartTick
	})
	return out
}

func sortConflicts(rep *normalize.Report) []normalize.Conflict {
	out := []normalize.Conflict{}
	if rep == nil {
		return out
	}
	out = append(out, rep.Conflicts...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].Field != out[j].Field {
			return out[i].Field < out[j].Field
		}
		return out[i].Representation < out[j].Representation
	})
	return out
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}

// percentile returns the nearest-rank percentile of a sorted copy.
func percentile(xs []float64, p float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	sorted := append([]float64(nil), xs...)
	sort.Float64s(sorted)
	rank := int(math.Ceil(p / 100 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}

func canonicalBytes(line *Line) []byte {
	clone := *line
	// The recovery counter is run-state, not analysis content: exclude it
	// alongside the hash itself so crash/replay produces an identical digest.
	clone.ReportSHA256 = ""
	clone.RecoveredTransactions = 0
	b, _ := json.Marshal(&clone)
	return b
}

func canonicalHash(line *Line) (string, error) {
	sum := sha256.Sum256(canonicalBytes(line))
	return hex.EncodeToString(sum[:]), nil
}

// Marshal emits the deterministic single-line JSON.
func (l *Line) Marshal() ([]byte, error) {
	b, err := json.Marshal(l)
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
