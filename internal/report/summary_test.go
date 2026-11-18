package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func sample() *Summary {
	return &Summary{
		Transport: "udp", Seed: 42,
		StateTransitions: 6, RejectedTransitions: 0,
		SequenceGaps: 1, MissingPackets: 1, DuplicatePackets: 1,
		ReassembledFrames: 5, IncompleteFrames: 0, ParseErrors: 1,
		Disconnected: false, DurationMS: 42,
	}
}

func TestWriteJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := sample().WriteJSON(&buf); err != nil {
		t.Fatalf("write json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got["transport"] != "udp" || got["state_transitions"] != float64(6) ||
		got["sequence_gaps"] != float64(1) || got["reassembled_frames"] != float64(5) {
		t.Fatalf("json fields wrong: %v", got)
	}
}

func TestWriteText(t *testing.T) {
	var buf bytes.Buffer
	if err := sample().WriteText(&buf); err != nil {
		t.Fatalf("write text: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"transport:", "state_transitions:", "sequence_gaps:",
		"reassembled_frames:", "parse_errors:",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("text output missing %q:\n%s", want, out)
		}
	}
}
