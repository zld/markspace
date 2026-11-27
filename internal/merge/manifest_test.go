package merge

import (
	"testing"

	"mediad/internal/clock"
)

func seg(key string, start, off int64, hash string) Segment {
	return Segment{TaskKey: key, StartNs: start, EndNs: start + 1000,
		SrcOffset: off, SrcLength: 64, SHA256: hash}
}

func TestMergeSortsByStableKey(t *testing.T) {
	m, err := Merge(
		[]Segment{seg("b", 0, 100, "h1")},
		[]Segment{seg("a", 0, 200, "h2")},
	)
	if err != nil {
		t.Fatal(err)
	}
	if m.Segments[0].TaskKey != "a" || m.Segments[1].TaskKey != "b" {
		t.Fatalf("not sorted by key: %+v", m.Segments)
	}
}

func TestMergeIdenticalDuplicateOnce(t *testing.T) {
	s := seg("k", 0, 100, "h")
	m, err := Merge([]Segment{s}, []Segment{s})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 1 {
		t.Fatalf("duplicate merged %d times, want 1", len(m.Segments))
	}
}

func TestMergeConflictingDuplicateErrors(t *testing.T) {
	a := seg("k", 0, 100, "h1")
	b := seg("k", 0, 100, "h2") // same key, different hash
	_, err := Merge([]Segment{a}, []Segment{b})
	if clock.CodeOf(err) != clock.ErrKeyConflict {
		t.Fatalf("want ERR_KEY_CONFLICT, got %v", err)
	}
}

func TestManifestDeterministicAcrossInputOrder(t *testing.T) {
	a, b := seg("a", 0, 100, "h1"), seg("b", 1000, 200, "h2")
	m1, err := Merge([]Segment{a, b})
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Merge([]Segment{b}, []Segment{a})
	if err != nil {
		t.Fatal(err)
	}
	h1, _ := m1.Hash()
	h2, _ := m2.Hash()
	if h1 != h2 {
		t.Fatalf("manifest hash depends on input order: %s != %s", h1, h2)
	}
}
