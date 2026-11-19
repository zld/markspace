// Package report renders pipeline results as stable, machine-parseable JSON.
// Reports contain only categories and identifiers — never keys or plaintext.
package report

import (
	"bytes"
	"encoding/json"
	"sort"

	"mediakey/internal/manifest"
)

// ReportVersion is bumped whenever the JSON schema changes incompatibly.
const ReportVersion = "1.0"

// Report is the top-level document emitted by every subcommand.
type Report struct {
	ReportVersion string                  `json:"report_version"`
	AssetID       string                  `json:"asset_id"`
	RotationCount int                     `json:"rotation_count"`
	ChunkResults  []manifest.ChunkResult  `json:"chunk_results"`
	Summary       manifest.Summary        `json:"summary"`
}

// Build assembles a report from per-chunk results. Results are sorted by chunk
// index so output is stable regardless of worker scheduling, and failure
// reasons are counted into the summary.
func Build(assetID string, rotationCount int, results []manifest.ChunkResult) *Report {
	sorted := make([]manifest.ChunkResult, len(results))
	copy(sorted, results)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].ChunkIndex < sorted[j].ChunkIndex
	})

	reasons := map[string]int{}
	ok, failed := 0, 0
	for _, r := range sorted {
		if r.Status == manifest.StatusOK {
			ok++
			continue
		}
		failed++
		if r.FailureReason != manifest.ReasonNone {
			reasons[string(r.FailureReason)]++
		}
	}
	return &Report{
		ReportVersion: ReportVersion,
		AssetID:       assetID,
		RotationCount: rotationCount,
		ChunkResults:  sorted,
		Summary: manifest.Summary{
			Total:   len(sorted),
			OK:      ok,
			Failed:  failed,
			Reasons: reasons,
		},
	}
}

// MarshalJSON returns indented JSON with a trailing newline. Go's encoding
// emits map keys (the reasons counter) in sorted order automatically.
func MarshalJSON(r *Report) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, err.Error())
	}
	return buf.Bytes(), nil
}
