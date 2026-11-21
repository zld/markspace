// Package report renders pipeline results as a stable, deterministically
// ordered JSON summary.
package report

import (
	"encoding/json"
	"fmt"
	"io"

	"mediaseal/internal/media"
	"mediaseal/internal/pipeline"
)

// Summary is the top-level report document. Records stay in input order;
// failure_counts keys are sorted by encoding/json.
type Summary struct {
	AssetID       string          `json:"asset_id"`
	RotationCount int             `json:"rotation_count"`
	KeyCount      int             `json:"key_count"`
	RecordCount   int             `json:"record_count"`
	AcceptedCount int             `json:"accepted_count"`
	RejectedCount int             `json:"rejected_count"`
	FailureCounts map[string]int  `json:"failure_counts"`
	Keys          []media.KeyInfo `json:"keys"`
	Records       []media.Record  `json:"records"`
}

// Build aggregates a pipeline result into a summary, counting accepted and
// rejected records and bucketing failures by reason.
func Build(res *pipeline.Result) Summary {
	s := Summary{
		AssetID:       res.AssetID,
		RotationCount: res.RotationCount,
		KeyCount:      len(res.Keys),
		RecordCount:   len(res.Records),
		FailureCounts: map[string]int{},
		Keys:          res.Keys,
		Records:       res.Records,
	}
	for _, rec := range res.Records {
		if rec.Status == media.StatusAccepted {
			s.AcceptedCount++
			continue
		}
		s.RejectedCount++
		s.FailureCounts[rec.FailureReason]++
	}
	return s
}

// WriteJSON writes the summary with stable ordering and indentation.
func WriteJSON(w io.Writer, s Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return nil
}
