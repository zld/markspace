// Package metrics aggregates bitrate, quality and manifest alignment
// statistics from executed frames and verifies cross-stage invariants.
package metrics

import (
	"mediainspect/internal/stream"
)

// BitrateStats summarizes actual vs target bitrate.
type BitrateStats struct {
	TargetKbps   float64 `json:"target_kbps"`
	ActualKbps   float64 `json:"actual_kbps"`
	DeviationPct float64 `json:"bitrate_deviation_pct"`
	DurationMs   float64 `json:"duration_ms"`
	TotalBits    int64   `json:"total_bits"`
}

// Bitrate computes the actual bitrate over the executed output. The media
// duration spans the first to the last output frame plus one frame period,
// so lost frames lower the measured bitrate instead of vanishing silently.
func Bitrate(outputs []stream.OutputFrame, targetKbps, frameMs float64) BitrateStats {
	st := BitrateStats{TargetKbps: targetKbps}
	if len(outputs) == 0 {
		return st
	}
	for _, o := range outputs {
		st.TotalBits += int64(o.Frame.Size) * 8
	}
	st.DurationMs = outputs[len(outputs)-1].TimestampMs - outputs[0].TimestampMs + frameMs
	if st.DurationMs > 0 {
		st.ActualKbps = float64(st.TotalBits) / st.DurationMs // bits/ms == kbps
	}
	if targetKbps > 0 {
		st.DeviationPct = (st.ActualKbps - targetKbps) / targetKbps * 100
	}
	return st
}
