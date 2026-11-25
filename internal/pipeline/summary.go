// Package pipeline wires the staged medpipe libraries into deterministic runs.
package pipeline

import (
	"fmt"

	"medpipe/internal/fault"
)

// Summary is the stable terminal summary consumed by acceptance checks.
type Summary struct {
	RoundtripPercent   float64 `json:"roundtrip_percent"`
	KeyframeHitPercent float64 `json:"keyframe_hit_percent"`
	Duplicates         int     `json:"duplicates"`
	Corrupt            int     `json:"corrupt"`
	Truncated          int     `json:"truncated"`
	Recovered          int     `json:"recovered"`
	SegmentSHA256      string  `json:"segment_sha256"`
}

// Line renders the exact key=value summary contract.
func (s Summary) Line() string {
	return fmt.Sprintf(
		"roundtrip=%.0f%% keyframe_hit=%.0f%% duplicates=%d corrupt=%d truncated=%d recovered=%d segment_sha256=%s",
		s.RoundtripPercent, s.KeyframeHitPercent, s.Duplicates, s.Corrupt,
		s.Truncated, s.Recovered, s.SegmentSHA256,
	)
}

// RunResult contains the summary plus machine-readable stage artifacts.
type RunResult struct {
	Summary        Summary
	Failures       []fault.Failure
	CheckpointPath string
	OutputDir      string
}
