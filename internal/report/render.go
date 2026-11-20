// Package report renders per-cue and summary results to the terminal
// and to JSON report files.
package report

import (
	"encoding/json"
	"fmt"
	"io"
)

// CueLine is one reported cue row.
type CueLine struct {
	CueID             string `json:"cue_id"`
	StartFrame        int64  `json:"start_frame"`
	EndFrameExclusive int64  `json:"end_frame_exclusive"`
	RoundtripDeltaMs  *int64 `json:"roundtrip_delta_ms"` // null for dropped cues
	Status            string `json:"status"`             // ok, trimmed, dropped
}

// FileReport is the outcome of converting one input file.
type FileReport struct {
	Input         string    `json:"input"`
	Output        string    `json:"output,omitempty"`
	Status        string    `json:"status"` // ok or error
	Error         string    `json:"error,omitempty"`
	InputCount    int       `json:"input_count"`
	OutputCount   int       `json:"output_count"`
	DroppedCount  int       `json:"dropped_count"`
	MaxAbsDeltaMs int64     `json:"max_abs_delta_ms"`
	Cues          []CueLine `json:"cues,omitempty"`
}

// RenderTerminal writes human-readable per-cue rows and a summary per file.
func RenderTerminal(w io.Writer, reps []FileReport) {
	for _, r := range reps {
		fmt.Fprintf(w, "input=%s output=%s status=%s\n", r.Input, r.Output, r.Status)
		if r.Error != "" {
			fmt.Fprintf(w, "error=%s\n", r.Error)
			continue
		}
		for _, c := range r.Cues {
			delta := "-"
			if c.RoundtripDeltaMs != nil {
				delta = fmt.Sprintf("%d", *c.RoundtripDeltaMs)
			}
			fmt.Fprintf(w, "cue_id=%s start_frame=%d end_frame_exclusive=%d roundtrip_delta_ms=%s status=%s\n",
				c.CueID, c.StartFrame, c.EndFrameExclusive, delta, c.Status)
		}
		fmt.Fprintf(w, "input_count=%d output_count=%d dropped_count=%d max_abs_delta_ms=%d\n",
			r.InputCount, r.OutputCount, r.DroppedCount, r.MaxAbsDeltaMs)
	}
}

// JSON renders the report set as indented JSON.
func JSON(reps []FileReport) ([]byte, error) {
	return json.MarshalIndent(struct {
		Files []FileReport `json:"files"`
	}{Files: reps}, "", "  ")
}
