package provenance

import (
	"path/filepath"
	"testing"
)

func sampleTrace(version string) *Trace {
	tr := New(version)
	tr.Add(Entry{
		SegmentIndex: 0,
		StartNanos:   0,
		EndNanos:     100,
		Inputs: []InputRef{
			{InputPath: "rep1/frag-001.m4s", SHA256: "b", MFHD: 1, TFDT: 0},
			{InputPath: "rep0/frag-001.m4s", SHA256: "a", MFHD: 1, TFDT: 0},
		},
	})
	tr.Add(Entry{
		SegmentIndex: 1,
		StartNanos:   100,
		EndNanos:     200,
		Inputs: []InputRef{
			{InputPath: "rep0/frag-002.m4s", SHA256: "c", MFHD: 2, TFDT: 10},
		},
	})
	return tr
}

func TestTraceDeterministicHash(t *testing.T) {
	h1, err := sampleTrace("v1").SHA256()
	if err != nil {
		t.Fatal(err)
	}
	h2, err := sampleTrace("v1").SHA256()
	if err != nil {
		t.Fatal(err)
	}
	if h1 != h2 {
		t.Fatalf("identical traces must hash equally: %s vs %s", h1, h2)
	}
	h3, err := sampleTrace("v2").SHA256()
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h3 {
		t.Fatal("different manifest versions must change the hash")
	}
}

func TestTraceWriteReadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "trace.json")
	orig := sampleTrace("v1")
	if err := orig.WriteJSON(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := ReadJSON(path)
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := orig.SHA256()
	h2, _ := loaded.SHA256()
	if h1 != h2 {
		t.Fatalf("round trip changed the trace: %s vs %s", h1, h2)
	}
	// Inputs must be stored sorted for determinism.
	if loaded.Entries[0].Inputs[0].InputPath != "rep0/frag-001.m4s" {
		t.Fatal("input refs were not sorted")
	}
}

func TestDiffIdenticalAndChanged(t *testing.T) {
	base := sampleTrace("v1")
	if diffs := Diff(base, sampleTrace("v1")); len(diffs) != 0 {
		t.Fatalf("identical traces must diff empty, got %+v", diffs)
	}
	changed := sampleTrace("v1")
	changed.Entries[0].StartNanos = 7
	changed.Entries[1].Inputs[0].SHA256 = "changed"
	diffs := Diff(base, changed)
	if len(diffs) != 2 {
		t.Fatalf("expected 2 field diffs, got %+v", diffs)
	}
	fields := map[string]bool{}
	for _, d := range diffs {
		fields[d.Field] = true
	}
	if !fields["start_nanos"] {
		t.Fatal("start_nanos diff missing")
	}
}

func TestDiffExtraSegment(t *testing.T) {
	base := sampleTrace("v1")
	extra := sampleTrace("v1")
	extra.Add(Entry{SegmentIndex: 2, StartNanos: 200, EndNanos: 300})
	diffs := Diff(base, extra)
	if len(diffs) != 1 || diffs[0].Field != "entry" {
		t.Fatalf("expected one entry-level diff, got %+v", diffs)
	}
}
