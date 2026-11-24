package provenance

import (
	"os"
	"path/filepath"
	"testing"

	"mediarun/internal/fault"
)

func sampleManifest() *Manifest {
	return &Manifest{
		Version:    Version,
		Tool:       "mediarun",
		ConfigHash: "abc",
		Timescale:  90000,
		Inputs:     []Input{{Name: "frames", Path: "frames.mfr", Bytes: 10, SHA256: "deadbeef"}},
		Stages: []StageSummary{{
			Name: "parse", InputHash: "i", ConfigHash: "c", OutputHash: "o",
			Counts: map[string]int{"records": 2},
		}},
		Segments: []Segment{
			{Index: 0, Path: "segments/segment-00.mp4", SHA256: "aa", Samples: 2},
			{Index: 1, Path: "segments/segment-01.mp4", SHA256: "bb", Samples: 2},
		},
		Faults: []FaultRecord{
			{Stage: "parse", Kind: "checksum_mismatch", Offset: 90, Record: 1},
			{Stage: "parse", Kind: "truncated_record", Offset: 10, Record: 0},
		},
	}
}

func TestCanonicalJSONIsStable(t *testing.T) {
	m := sampleManifest()
	a, err := CanonicalJSON(m)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		b, err := CanonicalJSON(m)
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Fatal("canonical JSON unstable")
		}
	}
	if a[len(a)-1] != '\n' {
		t.Fatal("missing trailing newline")
	}
}

func TestWriteAtomicReplacesAndNoTempLeft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	if err := WriteAtomic(path, sampleManifest()); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m := sampleManifest()
	m.Segments = m.Segments[:1]
	if err := WriteAtomic(path, m); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(path)
	if string(first) == string(second) {
		t.Fatal("manifest was not replaced")
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "manifest.json" {
			t.Fatalf("stray file left: %s", e.Name())
		}
	}
}

func TestWriteAtomicSortsFaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.json")
	m := sampleManifest()
	if err := WriteAtomic(path, m); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Faults[0].Offset != 10 {
		t.Fatalf("faults not sorted: %+v", loaded.Faults)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateRejectsBadVersionAndIndex(t *testing.T) {
	m := sampleManifest()
	m.Version = 99
	if err := m.Validate(); err == nil {
		t.Fatal("bad version accepted")
	}
	m = sampleManifest()
	m.Segments[1].Index = 5
	if err := m.Validate(); err == nil {
		t.Fatal("bad segment index accepted")
	}
	m = sampleManifest()
	m.Timescale = 0
	if err := m.Validate(); err == nil {
		t.Fatal("zero timescale accepted")
	}
}

func TestHashHelpers(t *testing.T) {
	if got := HashBytesHex([]byte("x")); len(got) != 64 {
		t.Fatalf("digest length = %d", len(got))
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	os.WriteFile(path, []byte("hello"), 0o644)
	sum, n, err := HashFile(path)
	if err != nil || n != 5 || len(sum) != 64 {
		t.Fatalf("HashFile = %q %d %v", sum, n, err)
	}
	if _, _, err := HashFile(filepath.Join(dir, "missing")); err == nil {
		t.Fatal("missing file hashed")
	}
}

func TestEqualHashesAndSummary(t *testing.T) {
	if !EqualHashes([]string{"a", "b"}, []string{"a", "b"}) {
		t.Fatal("equal slices differ")
	}
	if EqualHashes([]string{"a"}, []string{"a", "b"}) {
		t.Fatal("length mismatch accepted")
	}
	if EqualHashes([]string{"a"}, []string{"b"}) {
		t.Fatal("value mismatch accepted")
	}
	m := sampleManifest()
	m.Metrics.ParseErrors = 2
	line := m.Summarize()
	if line == "" || !contains(line, "parse_errors=2") {
		t.Fatalf("summary = %q", line)
	}
}

func TestFromFaultCopiesFields(t *testing.T) {
	e := fault.New(fault.StageChecksum, fault.KindChecksum, "bad").At(42, 7).WithTrack(1)
	e.Recoverable = true
	rec := FromFault(e)
	if rec.Stage != "checksum" || rec.Kind != "checksum_mismatch" || rec.Offset != 42 || rec.Track != 1 || !rec.Recoverable {
		t.Fatalf("record = %+v", rec)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
