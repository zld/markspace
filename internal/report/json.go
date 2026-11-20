// Package report renders the per-transport ingest statistics as stable,
// single-line JSON. Struct field order defines the key order, so identical
// inputs always produce byte-identical output across runs.
package report

import (
	"encoding/json"
	"fmt"
	"io"
)

// Line is the JSON object emitted once per transport.
type Line struct {
	Transport          string `json:"transport"`
	FinalState         string `json:"final_state"`
	StateTransitions   int    `json:"state_transitions"`
	FSMIllegal         int    `json:"fsm_illegal_transitions"`
	FSMDuplicateEvents int    `json:"fsm_duplicate_events"`
	PacketsReceived    uint64 `json:"packets_received"`
	RTPSeqGaps         uint64 `json:"rtp_seq_gaps"`
	DuplicatePackets   uint64 `json:"duplicate_packets"`
	LateOutOfWindow    uint64 `json:"late_out_of_window"`
	ReassembledFrames  uint64 `json:"reassembled_frames"`
	DiscardedFrames    uint64 `json:"discarded_frames"`
	MissingFragments   uint64 `json:"missing_fragments"`
	IllegalFUHeaders   uint64 `json:"illegal_fu_headers"`
	OversizedFrames    uint64 `json:"oversized_frames"`
	MalformedPackets   uint64 `json:"malformed_packets"`
	CompleteFrames     int    `json:"complete_frames"`
}

// WriteLine serialises one report object as a single JSON line to w.
func WriteLine(w io.Writer, l Line) error {
	buf, err := json.Marshal(l)
	if err != nil {
		return fmt.Errorf("report: marshal failed: %w", err)
	}
	buf = append(buf, '\n')
	if _, err := w.Write(buf); err != nil {
		return fmt.Errorf("report: write failed: %w", err)
	}
	return nil
}
