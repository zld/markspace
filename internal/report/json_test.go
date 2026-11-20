package report

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"

	"tsinspect/internal/demux"
	"tsinspect/internal/fixture"
)

func summaryFor(t *testing.T, scenario string) *Summary {
	t.Helper()
	path := filepath.Join(t.TempDir(), scenario+".ts")
	if err := fixture.Generate(path, scenario); err != nil {
		t.Fatal(err)
	}
	s, r, err := demux.ParseFile(path, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	return Build(path, s, r)
}

func TestJSONIsDeterministicAndSorted(t *testing.T) {
	path := filepath.Join(t.TempDir(), "updates-loss.ts")
	if err := fixture.Generate(path, fixture.ScenarioUpdatesLoss); err != nil {
		t.Fatal(err)
	}
	render := func() []byte {
		s, r, err := demux.ParseFile(path, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		b, err := EncodeJSON(Build(path, s, r))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	first, second := render(), render()
	if !bytes.Equal(first, second) {
		t.Fatal("JSON output is not deterministic")
	}
	var decoded map[string]any
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	for _, key := range []string{"pat_version_changes", "pmt_version_changes", "program_to_pmt_pid", "streams"} {
		if _, ok := decoded[key]; !ok {
			t.Fatalf("missing key %q", key)
		}
	}
}

func TestSummaryContents(t *testing.T) {
	s := summaryFor(t, fixture.ScenarioUpdatesLoss)
	if len(s.ProgramToPMTPID) < 2 {
		t.Fatalf("program_to_pmt_pid has %d entries", len(s.ProgramToPMTPID))
	}
	if s.PATVersionChanges != 1 || s.PMTVersionChanges != 2 {
		t.Fatalf("version changes PAT=%d PMT=%d", s.PATVersionChanges, s.PMTVersionChanges)
	}
	if s.ContinuityErrors == 0 {
		t.Fatal("expected continuity errors")
	}
	if s.TrailingBytes == 0 || s.TrailingPackets != 1 {
		t.Fatalf("trailing = %d bytes / %d packets", s.TrailingBytes, s.TrailingPackets)
	}
	// PID ordering is numeric and strictly increasing.
	for i := 1; i < len(s.PIDs); i++ {
		if s.PIDs[i-1].PID >= s.PIDs[i].PID {
			t.Fatalf("PIDs not sorted at %d: %d >= %d", i, s.PIDs[i-1].PID, s.PIDs[i].PID)
		}
	}
	for i, sr := range s.Streams {
		if sr.PacketCount < 0 {
			t.Fatalf("negative packet count for stream %d", i)
		}
		if sr.ProgramNumber == 0 || sr.PID == 0 {
			t.Fatalf("stream %d missing identifiers: %+v", i, sr)
		}
	}
	if len(s.Streams) < 4 {
		t.Fatalf("expected at least four streams, got %d", len(s.Streams))
	}
}

func TestSummaryUnknownStreamType(t *testing.T) {
	s := summaryFor(t, fixture.ScenarioCorrupt)
	found := false
	for _, sr := range s.Streams {
		if sr.Description == "unknown" {
			found = true
			if sr.StreamTypeHex != "0x99" {
				t.Fatalf("unknown stream type hex = %s", sr.StreamTypeHex)
			}
		}
	}
	if !found {
		t.Fatal("expected an unknown stream type from the corrupt scenario")
	}
}

func TestTextOutput(t *testing.T) {
	s := summaryFor(t, fixture.ScenarioBaseline)
	var buf bytes.Buffer
	if err := WriteText(&buf, s); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"program 1", "program 2", "packets", "continuity"} {
		if !bytes.Contains([]byte(out), []byte(want)) {
			t.Fatalf("text output missing %q:\n%s", want, out)
		}
	}
	// Guard against a nil-pointer regression when the file is empty.
}
