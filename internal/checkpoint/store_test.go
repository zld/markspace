package checkpoint

import (
	"path/filepath"
	"testing"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	rec := Record{
		NodeID:       "node-a",
		InputDigest:  "payload:abc",
		DepDigests:   map[string]string{"up": "payload:111"},
		ResultDigest: "container:xyz",
		Kind:         "subtitle",
		Payload:      []byte("cue data"),
	}
	if err := s.Save(rec); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load("node-a")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || got.ResultDigest != "container:xyz" || string(got.Payload) != "cue data" {
		t.Fatalf("round trip wrong: %+v", got)
	}
	// Record file lives inside the checkpoint dir, never outside it.
	if filepath.Dir(s.path("node-a")) != filepath.Join(dir, "checkpoints") {
		t.Fatal("checkpoint escaped its directory")
	}
}

func TestLoadMissingIsNotError(t *testing.T) {
	s, _ := Open(t.TempDir())
	got, err := s.Load("absent")
	if err != nil || got != nil {
		t.Fatalf("missing record: got=%v err=%v, want nil,nil", got, err)
	}
}

func TestValidationMismatchDiscards(t *testing.T) {
	s, _ := Open(t.TempDir())
	rec := Record{
		NodeID:       "n",
		InputDigest:  "payload:v1",
		DepDigests:   map[string]string{"up": "payload:d1"},
		ResultDigest: "container:r1",
	}
	if err := s.Save(rec); err != nil {
		t.Fatal(err)
	}
	// Same input and deps: valid.
	if !s.Valid(&rec, "payload:v1", map[string]string{"up": "payload:d1"}) {
		t.Fatal("matching record reported invalid")
	}
	// Input changed: invalid.
	if s.Valid(&rec, "payload:v2", map[string]string{"up": "payload:d1"}) {
		t.Fatal("input change not detected")
	}
	// Dependency changed: invalid.
	if s.Valid(&rec, "payload:v1", map[string]string{"up": "payload:d2"}) {
		t.Fatal("dependency change not detected")
	}
	// Extra/missing dependency: invalid.
	if s.Valid(&rec, "payload:v1", map[string]string{"up": "payload:d1", "more": "x"}) {
		t.Fatal("dependency count change not detected")
	}
	// Nil record is never valid.
	if s.Valid(nil, "payload:v1", nil) {
		t.Fatal("nil record reported valid")
	}
}

func TestDiscardAndCorruptFile(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	if err := s.Save(Record{NodeID: "n", InputDigest: "a", ResultDigest: "b"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Discard("n"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Load("n"); got != nil {
		t.Fatal("record survived discard")
	}
	// Discarding an absent record is a no-op.
	if err := s.Discard("never-existed"); err != nil {
		t.Fatalf("discard missing: %v", err)
	}
	// A corrupt JSON file degrades to "absent", not a hard failure.
	rec2 := Record{NodeID: "good", InputDigest: "a", ResultDigest: "b"}
	_ = s.Save(rec2)
	if err := writeTrash(s.path("good")); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load("good"); err != nil || got != nil {
		t.Fatalf("corrupt file should load as nil,nil; got %v %v", got, err)
	}
}

func TestAtomicWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	for i := 0; i < 5; i++ {
		if err := s.Save(Record{NodeID: "k", InputDigest: "a", ResultDigest: "b"}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := readDirNames(filepath.Join(dir, "checkpoints"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range entries {
		if filepath.Ext(name) == ".tmp" {
			t.Fatalf("temp file left behind: %s", name)
		}
	}
}

func TestAllAndSortedIDs(t *testing.T) {
	s, _ := Open(t.TempDir())
	for _, id := range []string{"z", "a", "m"} {
		if err := s.Save(Record{NodeID: id, InputDigest: "in", ResultDigest: "out"}); err != nil {
			t.Fatal(err)
		}
	}
	recs, err := s.All()
	if err != nil || len(recs) != 3 {
		t.Fatalf("All: recs=%d err=%v", len(recs), err)
	}
	ids := SortedIDs(recs)
	if len(ids) != 3 || ids[0] != "a" || ids[1] != "m" || ids[2] != "z" {
		t.Fatalf("sorted ids wrong: %v", ids)
	}
}

func TestEmptyNodeIDRejected(t *testing.T) {
	s, _ := Open(t.TempDir())
	if err := s.Save(Record{NodeID: ""}); err == nil {
		t.Fatal("empty node id accepted")
	}
}
