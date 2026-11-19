// Package report renders run results as JSON or a terminal summary.
// Ratio fields are explicit nulls when no valid frame was ever output.
package report

import (
	"encoding/json"
	"fmt"
	"io"

	"jitterlab/internal/metrics"
)

// Summary carries the aggregate counters for one run.
type Summary struct {
	Received         uint64   `json:"received"`
	DuplicateDropped uint64   `json:"duplicate_dropped"`
	LateDropped      uint64   `json:"late_dropped"`
	Malformed        uint64   `json:"malformed"`
	PlayedFrames     uint64   `json:"played_frames"`
	ConcealedFrames  uint64   `json:"concealed_frames"`
	MaxConcealRun    int      `json:"max_conceal_run"`
	Unrecoverable    uint64   `json:"unrecoverable_events"`
	ContinuityRatio  *float64 `json:"continuity_ratio"`
	TargetP50Ms      *float64 `json:"target_delay_p50_ms"`
	TargetP95Ms      *float64 `json:"target_delay_p95_ms"`
}

// Report is the top-level result document.
type Report struct {
	Mode       string              `json:"mode"`
	Summary    Summary             `json:"summary"`
	Watermarks []metrics.Watermark `json:"watermarks"`
}

// Build aggregates metrics into a Report.
func Build(mode string, m *metrics.Metrics) Report {
	r := Report{
		Mode: mode,
		Summary: Summary{
			Received:         m.Received,
			DuplicateDropped: m.DuplicateDropped,
			LateDropped:      m.LateDropped,
			Malformed:        m.Malformed,
			PlayedFrames:     m.Played,
			ConcealedFrames:  m.Concealed,
			MaxConcealRun:    m.MaxConcealRun,
			Unrecoverable:    m.Unrecoverable,
		},
		Watermarks: m.Watermarks,
	}
	if v, ok := m.Continuity(); ok {
		r.Summary.ContinuityRatio = &v
	}
	if v, ok := m.TargetQuantile(0.50); ok {
		r.Summary.TargetP50Ms = &v
	}
	if v, ok := m.TargetQuantile(0.95); ok {
		r.Summary.TargetP95Ms = &v
	}
	if r.Watermarks == nil {
		r.Watermarks = []metrics.Watermark{}
	}
	return r
}

// PrintJSON writes the report as indented JSON.
func PrintJSON(w io.Writer, r Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// PrintText writes a compact terminal summary (no payload data, no full
// watermark series — just its length).
func PrintText(w io.Writer, r Report) {
	s := r.Summary
	fmt.Fprintf(w, "mode:               %s\n", r.Mode)
	fmt.Fprintf(w, "received:           %d\n", s.Received)
	fmt.Fprintf(w, "duplicate_dropped:  %d\n", s.DuplicateDropped)
	fmt.Fprintf(w, "late_dropped:       %d\n", s.LateDropped)
	fmt.Fprintf(w, "malformed:          %d\n", s.Malformed)
	fmt.Fprintf(w, "played_frames:      %d\n", s.PlayedFrames)
	fmt.Fprintf(w, "concealed_frames:   %d\n", s.ConcealedFrames)
	fmt.Fprintf(w, "max_conceal_run:    %d\n", s.MaxConcealRun)
	fmt.Fprintf(w, "unrecoverable:      %d\n", s.Unrecoverable)
	fmt.Fprintf(w, "continuity_ratio:   %s\n", fptr(s.ContinuityRatio))
	fmt.Fprintf(w, "target_delay_p50:   %s ms\n", fptr(s.TargetP50Ms))
	fmt.Fprintf(w, "target_delay_p95:   %s ms\n", fptr(s.TargetP95Ms))
	fmt.Fprintf(w, "watermark_samples:  %d\n", len(r.Watermarks))
}

func fptr(f *float64) string {
	if f == nil {
		return "null"
	}
	return fmt.Sprintf("%.3f", *f)
}
