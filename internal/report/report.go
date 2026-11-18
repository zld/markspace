// Package report defines the stable, field-ordered run summary emitted by the
// CLI and renders it as both deterministic text and indented JSON.
package report

import (
	"encoding/json"
	"strconv"
)

// Summary is the machine- and human-readable result of a generate/validate
// run. Every field carries an explicit JSON tag so field order is fixed by
// declaration order and never changes as the struct evolves.
type Summary struct {
	Status              string             `json:"status"`
	Representations     int                `json:"representations"`
	Segments            int                `json:"segments"`
	Timescale           int                `json:"timescale"`
	DurationMs          float64            `json:"duration_ms"`
	MaxAlignmentErrorMs float64            `json:"max_alignment_error_ms"`
	ToleranceMs         float64            `json:"tolerance_ms"`
	Renditions          []RenditionSummary `json:"renditions"`
	Findings            []FindingJSON      `json:"findings,omitempty"`
}

// RenditionSummary reports one representation's expansion outcome.
type RenditionSummary struct {
	ID           string `json:"id"`
	Bandwidth    int    `json:"bandwidth"`
	SegmentCount int    `json:"segment_count"`
	FirstSegment string `json:"first_segment"`
	LastSegment  string `json:"last_segment"`
}

// FindingJSON is the stable serialization shape of validator findings.
type FindingJSON struct {
	Rendition  string  `json:"rendition,omitempty"`
	Kind       string  `json:"kind"`
	Detail     string  `json:"detail"`
	BoundaryMs float64 `json:"boundary_ms,omitempty"`
	ErrorMs    float64 `json:"error_ms,omitempty"`
}

// JSON renders the summary with stable field ordering and indentation.
func (s Summary) JSON() ([]byte, error) {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// Text renders the human-readable form. The key metrics use the same names as
// the JSON form so terminal output and scripts tell the same story.
func (s Summary) Text() string {
	out := "dashforge " + s.Status + "\n" +
		"representations=" + strconv.Itoa(s.Representations) + "\n" +
		"segments=" + strconv.Itoa(s.Segments) + "\n" +
		"timescale=" + strconv.Itoa(s.Timescale) + "\n" +
		"duration_ms=" + strconv.FormatFloat(s.DurationMs, 'f', -1, 64) + "\n" +
		"max_alignment_error_ms=" + strconv.FormatFloat(s.MaxAlignmentErrorMs, 'f', -1, 64) + "\n"
	for _, r := range s.Renditions {
		out += "rendition " + r.ID +
			" bandwidth=" + strconv.Itoa(r.Bandwidth) +
			" segments=" + strconv.Itoa(r.SegmentCount) +
			" first=" + r.FirstSegment +
			" last=" + r.LastSegment + "\n"
	}
	for _, f := range s.Findings {
		line := "finding " + f.Kind + ": " + f.Detail
		if f.Rendition != "" {
			line = "rendition " + f.Rendition + " " + line
		}
		if f.ErrorMs != 0 {
			line += " (boundary=" + strconv.FormatFloat(f.BoundaryMs, 'f', -1, 64) +
				"ms, error=" + strconv.FormatFloat(f.ErrorMs, 'f', -1, 64) + "ms)"
		}
		out += line + "\n"
	}
	return out
}
