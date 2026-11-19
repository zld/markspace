package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func sample() Report {
	return Report{
		Mode: "tcp", StateTransitions: 6, ReceivedPackets: 480,
		SequenceGaps: 3, CompleteFrames: 110, IncompleteFrames: 7,
		FinalState: "Closed", Seed: 41, FramesRequested: 120,
	}
}

func TestWriteJSONStableFields(t *testing.T) {
	var buf bytes.Buffer
	if err := sample().Write(&buf, FormatJSON); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, raw)
	}
	for _, key := range []string{
		"mode", "state_transitions", "received_packets", "sequence_gaps",
		"duplicate_packets", "malformed_packets", "complete_frames",
		"incomplete_frames",
	} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing key %s", key)
		}
	}

	// Re-render must be byte-identical (stable ordering).
	var buf2 bytes.Buffer
	if err := sample().Write(&buf2, FormatJSON); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, buf2.Bytes()) {
		t.Fatal("json rendering is not deterministic")
	}
	// Explicit key order: mode first, frames_requested last.
	s := strings.TrimSpace(buf.String())
	if !strings.HasPrefix(s, "{\n  \"mode\":") {
		t.Fatalf("unexpected first key: %q", s[:20])
	}
	if !strings.HasSuffix(s, "\"frames_requested\": 120\n}") {
		t.Fatalf("unexpected tail: %s", s)
	}
}

func TestWriteSummarySingleLine(t *testing.T) {
	var buf bytes.Buffer
	if err := sample().Write(&buf, FormatSummary); err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(buf.String())
	if strings.Contains(line, "\n") {
		t.Fatal("summary must be a single line")
	}
	for _, want := range []string{
		"mode=tcp", "state_transitions=6", "sequence_gaps=3",
		"complete_frames=110", "final_state=Closed", "seed=41",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("summary missing %s: %s", want, line)
		}
	}
}

func TestParseFormat(t *testing.T) {
	if _, err := ParseFormat("xml"); err == nil {
		t.Fatal("xml must be rejected")
	}
	if f, err := ParseFormat("json"); err != nil || f != FormatJSON {
		t.Fatalf("json parse: %v %v", f, err)
	}
}
