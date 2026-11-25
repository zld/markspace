package batch

import (
	"path/filepath"
	"testing"

	"mediascan/internal/checkpoint"
	"mediascan/internal/fingerprint"
)

func newTestStore(t *testing.T) *checkpoint.Store {
	t.Helper()
	s, err := checkpoint.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func identity() checkpoint.Identity {
	return checkpoint.Identity{
		InputHash:    "in-1",
		ManifestHash: "man-1",
		RuleVersion:  fingerprint.RuleVersion,
	}
}

// alignedObjects builds two representations whose per-segment boundaries
// match exactly on the millisecond scale (90k and 30k timescales, same time).
func alignedObjects(n int) []Object {
	var objs []Object
	for i := 0; i < n; i++ {
		objs = append(objs,
			Object{
				Source: "hi", RepID: "high", SegmentIndex: int64(i), TrackID: 1,
				Timescale: 90000, TimeStart: int64(i) * 9000, TimeEnd: int64(i+1) * 9000,
				Data: []byte("payload-hi"), Keyframe: i == 0,
			},
			Object{
				Source: "lo", RepID: "low", SegmentIndex: int64(i), TrackID: 2,
				Timescale: 30000, TimeStart: int64(i) * 3000, TimeEnd: int64(i+1) * 3000,
				Data: []byte("payload-lo"), Keyframe: i == 0,
			},
		)
	}
	return objs
}

func TestRunAlignedNoMisalignment(t *testing.T) {
	store := newTestStore(t)
	r := NewRunner(store, 2, identity())
	sum, err := r.Run(alignedObjects(3))
	if err != nil {
		t.Fatal(err)
	}
	if sum.TotalSegments != 6 || sum.Misalignments != 0 {
		t.Fatalf("total=%d misalign=%d", sum.TotalSegments, sum.Misalignments)
	}
	if r.Misalignments() != 0 {
		t.Fatalf("misalignment counter = %d", r.Misalignments())
	}
	if sum.KeyframeHits != 2 || sum.KeyframeWanted != 2 {
		t.Fatalf("keyframes hits=%d wanted=%d", sum.KeyframeHits, sum.KeyframeWanted)
	}
	// Distinct payloads stay distinct (no false dedup across reps).
	if len(sum.Hashes) != 2 {
		t.Fatalf("distinct hashes = %d, want 2", len(sum.Hashes))
	}
}

func TestMisalignmentBeyond20ms(t *testing.T) {
	store := newTestStore(t)
	r := NewRunner(store, 4, identity())
	objs := alignedObjects(2)
	// Shorten the low representation's second segment by 30 ms: high ends
	// at 200 ms cumulative while low ends at 170 ms, a 30 ms divergence.
	objs[3].TimeStart += 900                   // 3000 -> 3900
	objs[3].TimeEnd = objs[3].TimeStart + 2100 // 70 ms duration instead of 100
	sum, err := r.Run(objs)
	if err != nil {
		t.Fatal(err)
	}
	if r.Misalignments() == 0 || sum.Misalignments == 0 {
		t.Fatal("30ms divergence must be flagged as a misalignment")
	}
	if len(r.Errors()) == 0 {
		t.Fatal("misalignment must record a structured timeline error")
	}
}

func TestCorruptObjectNotIndexed(t *testing.T) {
	store := newTestStore(t)
	r := NewRunner(store, 2, identity())
	objs := alignedObjects(2)
	objs[2].Corrupt = true
	objs[2].CorruptOffset = 77
	sum, err := r.Run(objs)
	if err != nil {
		t.Fatal(err)
	}
	// One corrupt object out of four is excluded from the index.
	if sum.IndexedSegments != 3 {
		t.Fatalf("indexed = %d, want 3 (corrupt object excluded)", sum.IndexedSegments)
	}
	if !r.Index().IsRejected("hi") {
		t.Fatal("corrupt source must be marked rejected")
	}
	found := false
	for _, e := range r.Errors() {
		if e.Offset == 77 {
			found = true
		}
	}
	if !found {
		t.Fatal("corrupt object must report its locatable offset 77")
	}
	// Checkpoint still committed despite the partial failure.
	if store.CommittedCount() == 0 {
		t.Fatal("committed checkpoints must survive a partial batch failure")
	}
}

func TestResumeReusesCommittedAndRedoesBreakpoint(t *testing.T) {
	dir := t.TempDir()
	objs := alignedObjects(4) // 8 objects, chunk size 2 => 4 chunks

	// First run: simulate an interruption after the first two chunks by
	// committing them, then verify a fresh runner resumes from chunk 2.
	first, err := checkpoint.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r1 := NewRunner(first, 2, identity())
	sum1, err := r1.Run(objs[:4]) // only first 4 objects = 2 chunks
	if err != nil {
		t.Fatal(err)
	}

	// Second run over the full set reuses the two committed chunks.
	second, err := checkpoint.Open(filepath.Join(dir))
	if err != nil {
		t.Fatal(err)
	}
	r2 := NewRunner(second, 2, identity())
	sum2, err := r2.Run(objs)
	if err != nil {
		t.Fatal(err)
	}
	if r2.ReusedChunks() != 2 {
		t.Fatalf("reused chunks = %d, want 2", r2.ReusedChunks())
	}
	// Reused + redone output must equal a continuous full run's hashes.
	full, err := checkpoint.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r3 := NewRunner(full, 2, identity())
	sum3, err := r3.Run(objs)
	if err != nil {
		t.Fatal(err)
	}
	if hashList(sum2.Hashes) != hashList(sum3.Hashes) {
		t.Fatal("resumed run hashes differ from a continuous full run")
	}
	if len(sum2.Hashes) != len(sum1.Hashes) && len(sum2.Hashes) < 2 {
		t.Fatal("resumed run lost earlier hashes")
	}
}

func TestIdentityChangeRedoesEverything(t *testing.T) {
	dir := t.TempDir()
	objs := alignedObjects(2)
	first, _ := checkpoint.Open(dir)
	r1 := NewRunner(first, 2, identity())
	if _, err := r1.Run(objs); err != nil {
		t.Fatal(err)
	}
	second, _ := checkpoint.Open(dir)
	changed := identity()
	changed.InputHash = "in-2"
	r2 := NewRunner(second, 2, changed)
	if _, err := r2.Run(objs); err != nil {
		t.Fatal(err)
	}
	if r2.ReusedChunks() != 0 {
		t.Fatalf("changed identity must invalidate all checkpoints, reused=%d",
			r2.ReusedChunks())
	}
}

func TestDedupAcrossIdenticalContent(t *testing.T) {
	store := newTestStore(t)
	r := NewRunner(store, 5, identity())
	objs := []Object{
		{Source: "a/seg0", RepID: "a", SegmentIndex: 0, TrackID: 1,
			Timescale: 1000, TimeStart: 0, TimeEnd: 100, Data: []byte("same-bytes")},
		{Source: "b/seg0", RepID: "b", SegmentIndex: 0, TrackID: 2,
			Timescale: 1000, TimeStart: 0, TimeEnd: 100, Data: []byte("same-bytes")},
	}
	sum, err := r.Run(objs)
	if err != nil {
		t.Fatal(err)
	}
	if len(sum.Hashes) != 1 || len(sum.Hashes[0].Refs) != 2 {
		t.Fatalf("identical content must dedup to one record with two refs: %+v", sum.Hashes)
	}
	if len(sum.Hashes[0].Refs) != 2 {
		t.Fatalf("merged record must keep both source offsets, got %d",
			len(sum.Hashes[0].Refs))
	}
}

func hashList(records []fingerprint.Record) string {
	out := ""
	for _, rec := range records {
		out += rec.Hash + ","
	}
	return out
}
