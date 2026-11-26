package audit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"replaykit/media/fault"
)

func TestHashBytesDeterministicAndDistinct(t *testing.T) {
	a := HashBytes([]byte("replaykit"))
	b := HashBytes([]byte("replaykit"))
	if a != b || len(a) != 64 {
		t.Fatalf("hash unstable/malformed: %q %q", a, b)
	}
	if HashBytes([]byte("replayKIT")) == a {
		t.Fatal("different input must hash differently")
	}
}

func TestHashFileMatchesBytes(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.bin")
	if err := os.WriteFile(p, []byte("segment-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if want := HashBytes([]byte("segment-bytes")); got != want {
		t.Fatalf("file hash %s != bytes hash %s", got, want)
	}
}

func TestReportFinalizeSortsAndSequences(t *testing.T) {
	r := NewReport("t")
	// Insert out of order; Finalize must give a stable sorted manifest.
	r.Add(fault.New("b-layer", 20, fault.CategoryPacketLost, fault.SeverityError, fault.ActionNone, 0, fault.Range{}, "z"))
	r.Add(fault.New("a-layer", 9, fault.CategoryTruncatedBox, fault.SeverityError, fault.ActionSkipped, 0, fault.Range{}, "a"))
	r.Add(fault.New("a-layer", 3, fault.CategoryBoxOverflow, fault.SeverityError, fault.ActionSkipped, 0, fault.Range{}, "c"))
	r.Finalize()
	if len(r.Anomalies) != 3 {
		t.Fatalf("anomalies=%d want 3", len(r.Anomalies))
	}
	for i, a := range r.Anomalies {
		if a.Sequence != i+1 {
			t.Fatalf("seq at %d = %d want %d", i, a.Sequence, i+1)
		}
	}
	if r.Anomalies[0].Layer != "a-layer" || r.Anomalies[0].Offset != 3 {
		t.Fatalf("sort wrong first row: %+v", r.Anomalies[0])
	}
	if r.Metrics.Anomalies != 3 {
		t.Fatalf("metrics anomalies=%d want 3", r.Metrics.Anomalies)
	}
}

func TestReportWriteJSONRoundTrip(t *testing.T) {
	r := NewReport("json")
	r.Metrics.Segments = 6
	r.Metrics.HashMatched = 6
	r.Metrics.HashTotal = 6
	r.Segments = append(r.Segments, SegmentRecord{Key: "e1/720p", HashMatch: true, State: StateBuilt})
	r.Finalize()
	p := filepath.Join(t.TempDir(), "nested", "report.json")
	if err := r.WriteJSON(p); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if got.Metrics.Segments != 6 || len(got.Segments) != 1 {
		t.Fatalf("roundtrip lost data: %+v", got.Metrics)
	}
}
