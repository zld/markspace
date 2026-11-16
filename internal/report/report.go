// Package report renders per-mode and total audit summaries as
// terminal text or JSON.
package report

import (
	"encoding/json"
	"fmt"
	"io"

	"rtsp-audit/internal/rtsp"
)

// ModeResult is the flat per-transport summary.
type ModeResult struct {
	TransitionCount     int `json:"transition_count"`
	SeqGapCount         int `json:"seq_gap_count"`
	FrameCount          int `json:"frame_count"`
	DroppedFrameCount   int `json:"dropped_frame_count"`
	DuplicateCount      int `json:"duplicate_count"`
	ReorderedCount      int `json:"reordered_count"`
	LateCount           int `json:"late_count"`
	WraparoundCount     int `json:"wraparound_count"`
	RejectedTransitions int `json:"rejected_transitions"`
	BadPayloads         int `json:"bad_payloads"`
	SSRCChanges         int `json:"ssrc_changes"`
}

// Add accumulates o into r.
func (r *ModeResult) Add(o ModeResult) {
	r.TransitionCount += o.TransitionCount
	r.SeqGapCount += o.SeqGapCount
	r.FrameCount += o.FrameCount
	r.DroppedFrameCount += o.DroppedFrameCount
	r.DuplicateCount += o.DuplicateCount
	r.ReorderedCount += o.ReorderedCount
	r.LateCount += o.LateCount
	r.WraparoundCount += o.WraparoundCount
	r.RejectedTransitions += o.RejectedTransitions
	r.BadPayloads += o.BadPayloads
	r.SSRCChanges += o.SSRCChanges
}

// FromStats converts client statistics into a ModeResult.
func FromStats(s *rtsp.Stats) ModeResult {
	if s == nil {
		return ModeResult{}
	}
	return ModeResult{
		TransitionCount:     s.Transitions,
		SeqGapCount:         s.RTP.Gaps,
		FrameCount:          s.H264.Frames,
		DroppedFrameCount:   s.H264.DroppedFrames,
		DuplicateCount:      s.RTP.Duplicates,
		ReorderedCount:      s.RTP.Reordered,
		LateCount:           s.RTP.Late,
		WraparoundCount:     s.RTP.Wraps,
		RejectedTransitions: s.RejectedTransitions,
		BadPayloads:         s.H264.BadPayloads,
		SSRCChanges:         s.H264.SSRCChanges,
	}
}

// Summary aggregates all modes of one run.
type Summary struct {
	Modes map[string]ModeResult `json:"modes"`
	Total ModeResult            `json:"total"`
}

// NewSummary builds the summary from per-mode results in order.
func NewSummary(modes []string, results []ModeResult) Summary {
	s := Summary{Modes: make(map[string]ModeResult, len(modes))}
	for i, m := range modes {
		s.Modes[m] = results[i]
		s.Total.Add(results[i])
	}
	return s
}

// PrintJSON writes the summary as indented JSON.
func PrintJSON(w io.Writer, s Summary) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(s)
}

// PrintText writes a terminal table of the summary.
func PrintText(w io.Writer, order []string, s Summary) {
	fmt.Fprintf(w, "%-16s %11s %8s %7s %8s %5s %9s %9s\n",
		"mode", "transitions", "seq_gap", "frames", "dropped", "dup", "reordered", "ssrc_chg")
	for _, m := range order {
		r := s.Modes[m]
		fmt.Fprintf(w, "%-16s %11d %8d %7d %8d %5d %9d %9d\n",
			m, r.TransitionCount, r.SeqGapCount, r.FrameCount, r.DroppedFrameCount,
			r.DuplicateCount, r.ReorderedCount, r.SSRCChanges)
	}
	fmt.Fprintf(w, "%-16s %11d %8d %7d %8d %5d %9d %9d\n",
		"total", s.Total.TransitionCount, s.Total.SeqGapCount, s.Total.FrameCount,
		s.Total.DroppedFrameCount, s.Total.DuplicateCount, s.Total.ReorderedCount, s.Total.SSRCChanges)
}
