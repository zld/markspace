// Package report aggregates diagnostic counters into JSON or text output.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Summary is the final per-run diagnostic report.
type Summary struct {
	Transport         string `json:"transport"`
	Seed              int64  `json:"seed"`
	StateTransitions  int    `json:"state_transitions"`
	RejectedTransitions int  `json:"rejected_transitions"`
	SequenceGaps      int    `json:"sequence_gaps"`
	MissingPackets    int    `json:"missing_packets"`
	DuplicatePackets  int    `json:"duplicate_packets"`
	LatePackets       int    `json:"late_packets"`
	TimeoutFlushes    int    `json:"reorder_timeout_flushes"`
	ReassembledFrames int    `json:"reassembled_frames"`
	IncompleteFrames  int    `json:"incomplete_frames"`
	ParseErrors       int    `json:"parse_errors"`
	Disconnected      bool   `json:"server_disconnected"`
	DurationMS        int64  `json:"duration_ms"`
}

// WriteJSON emits the summary as pretty-printed JSON.
func (s *Summary) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(s); err != nil {
		return fmt.Errorf("report: encode json: %w", err)
	}
	return nil
}

// WriteText emits a human readable terminal summary.
func (s *Summary) WriteText(w io.Writer) error {
	lines := []struct{ k, v string }{
		{"transport", s.Transport},
		{"seed", fmt.Sprintf("%d", s.Seed)},
		{"state_transitions", fmt.Sprintf("%d", s.StateTransitions)},
		{"rejected_transitions", fmt.Sprintf("%d", s.RejectedTransitions)},
		{"sequence_gaps", fmt.Sprintf("%d", s.SequenceGaps)},
		{"missing_packets", fmt.Sprintf("%d", s.MissingPackets)},
		{"duplicate_packets", fmt.Sprintf("%d", s.DuplicatePackets)},
		{"late_packets", fmt.Sprintf("%d", s.LatePackets)},
		{"reorder_timeout_flushes", fmt.Sprintf("%d", s.TimeoutFlushes)},
		{"reassembled_frames", fmt.Sprintf("%d", s.ReassembledFrames)},
		{"incomplete_frames", fmt.Sprintf("%d", s.IncompleteFrames)},
		{"parse_errors", fmt.Sprintf("%d", s.ParseErrors)},
		{"server_disconnected", fmt.Sprintf("%t", s.Disconnected)},
		{"duration_ms", fmt.Sprintf("%d", s.DurationMS)},
	}
	for _, l := range lines {
		if _, err := fmt.Fprintf(w, "%-24s %s\n", l.k+":", l.v); err != nil {
			return err
		}
	}
	return nil
}

// Duration converts a run duration into the millisecond field.
func DurationMS(d time.Duration) int64 { return d.Milliseconds() }
