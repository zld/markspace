package pipeline

import (
	"errors"
	"fmt"
	"testing"

	"regress-kernel/internal/media"
)

func fmtSeg(n int) string { return fmt.Sprintf("seg-%05d.m4s", n) }

func sampleEntries(task string, n int) []SegmentEntry {
	out := make([]SegmentEntry, n)
	for i := range out {
		out[i] = SegmentEntry{
			TaskKey: task, Rendition: task, Name: fmtSeg(i + 1),
			StartTick: int64(i) * 1000, Size: 100,
			SHA256: media.ComputeSHA256([]byte(task + fmtSeg(i+1))),
		}
	}
	return out
}

func TestMergeDeterministicBytes(t *testing.T) {
	a := sampleEntries("task-a", 3)
	b := sampleEntries("task-b", 2)
	// Deliver in scrambled order; output order must still be key/time sorted.
	m1, err := MergeManifest("normal", 17, b, a)
	if err != nil {
		t.Fatal(err)
	}
	m2, err := MergeManifest("normal", 17, a, b)
	if err != nil {
		t.Fatal(err)
	}
	equal, err := EqualManifestBytes(m1, m2)
	if err != nil || !equal {
		t.Fatalf("manifest bytes not delivery-order independent: %v", err)
	}
	if m1.Entries[0].TaskKey != "task-a" {
		t.Fatal("entries not sorted by task key")
	}
	h1, _ := m1.ManifestSHA256()
	h2, _ := m2.ManifestSHA256()
	if h1 != h2 {
		t.Fatal("manifest SHA-256 drift across merges")
	}
}

func TestMergeDuplicateDeliveryDedup(t *testing.T) {
	base := sampleEntries("task-a", 3)
	m, err := MergeManifest("normal", 17, base, base) // redelivered whole batch
	if err != nil {
		t.Fatalf("identical redelivery must not conflict: %v", err)
	}
	if len(m.Entries) != 3 {
		t.Fatalf("duplicates not collapsed: %d entries", len(m.Entries))
	}
}

func TestMergeConflictOnDigest(t *testing.T) {
	base := sampleEntries("task-a", 3)
	conflict := append([]SegmentEntry(nil), base...)
	conflict[1].SHA256 = media.ComputeSHA256([]byte("tampered"))
	_, err := MergeManifest("normal", 17, base, conflict)
	if !errors.Is(err, media.ErrManifestConflict) {
		t.Fatalf("want ErrManifestConflict, got %v", err)
	}
}

func TestManifestContinuityCheck(t *testing.T) {
	good := sampleEntries("task-a", 4)
	if err := Continuity(good, 1000); err != nil {
		t.Fatalf("continuous track rejected: %v", err)
	}
	bad := append([]SegmentEntry(nil), good...)
	bad[2].StartTick += 500 // introduce an overlap
	if err := Continuity(bad, 1000); !errors.Is(err, media.ErrCheckpoint) {
		t.Fatalf("want continuity error, got %v", err)
	}
}

func TestManifestRerunIdentical(t *testing.T) {
	// Simulate two whole program runs with the same stable inputs.
	entries := sampleEntries("task-a", 5)
	first, _ := MergeManifest("normal", 17, entries)
	second, _ := MergeManifest("normal", 17, entries)
	b1, _ := first.Marshal()
	b2, _ := second.Marshal()
	if string(b1) != string(b2) {
		t.Fatal("repeated runs produced different manifest bytes")
	}
}
