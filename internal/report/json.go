// Package report renders run results with stable field ordering. JSON is
// emitted field by field so the key order is part of the output contract
// and never depends on map iteration.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
)

// Line is one transport's summary in the exact wire field order.
type Line struct {
	Transport         string `json:"transport"`
	StateTransitions int    `json:"state_transitions"`
	RTPSeqGaps        int    `json:"rtp_seq_gaps"`
	FramesReassembled int    `json:"frames_reassembled"`
	FramesDropped     int    `json:"frames_dropped"`
	DuplicatePackets  int    `json:"duplicate_packets"`
}

// FromSummary projects any type exposing the needed counters into a Line.
// Keeping the projection explicit avoids importing transport into report.
func FromSummary(transport string, transitions, gaps, reassembled, dropped, duplicates int) Line {
	return Line{
		Transport:         transport,
		StateTransitions: transitions,
		RTPSeqGaps:        gaps,
		FramesReassembled: reassembled,
		FramesDropped:     dropped,
		DuplicatePackets:  duplicates,
	}
}

// Marshal returns deterministic JSON. json.Marshal on a struct preserves
// declaration order, and all fields are fixed-width integers or one of the
// two known transport strings, so output is reproducible.
func (l Line) Marshal() ([]byte, error) {
	b, err := json.Marshal(l)
	if err != nil {
		return nil, fmt.Errorf("report: marshal: %w", err)
	}
	return b, nil
}

// WriteLines emits one compact JSON object per line in the given order.
func WriteLines(w io.Writer, lines []Line) error {
	for _, l := range lines {
		b, err := l.Marshal()
		if err != nil {
			return err
		}
		if _, err := w.Write(b); err != nil {
			return fmt.Errorf("report: write: %w", err)
		}
		if _, err := io.WriteString(w, "\n"); err != nil {
			return fmt.Errorf("report: write newline: %w", err)
		}
	}
	return nil
}

// Exit codes are part of the terminal contract.
const (
	ExitOK            = 0
	ExitConfigError   = 2
	ExitRTSPFailure   = 3
	ExitPacketFailure = 4
)

// FormatError renders a stage error for stderr without leaking a stack
// trace; the stage field lets operators locate the failing phase.
func FormatError(stage string, err error) string {
	if stage == "" {
		return "error: " + err.Error()
	}
	return "error in rtsp stage " + strconv.Quote(stage) + ": " + err.Error()
}
