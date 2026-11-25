package index

import (
	"testing"

	"mediascan/internal/errs"
)

func TestAddAndFinalizeSorts(t *testing.T) {
	idx := New()
	entries := []Keyframe{
		{TrackID: 2, SampleNumber: 1, SegmentIndex: 0, ByteOffset: 100},
		{TrackID: 1, SampleNumber: 5, SegmentIndex: 1, ByteOffset: 300},
		{TrackID: 1, SampleNumber: 1, SegmentIndex: 0, ByteOffset: 50},
	}
	for _, e := range entries {
		if err := idx.Add(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := idx.Finalize(); err != nil {
		t.Fatal(err)
	}
	got := idx.Snapshot()
	// Sorted by track, then segment, then sample.
	want := []Keyframe{entries[2], entries[1], entries[0]}
	for i := range want {
		if got[i].TrackID != want[i].TrackID || got[i].SampleNumber != want[i].SampleNumber {
			t.Fatalf("position %d = %+v, want %+v", i, got[i], want[i])
		}
		if got[i].State != StateKnown {
			t.Fatalf("default state must be known, got %q", got[i].State)
		}
	}
}

func TestDuplicateRejected(t *testing.T) {
	idx := New()
	_ = idx.Add(Keyframe{TrackID: 1, SampleNumber: 1, SegmentIndex: 0})
	_ = idx.Add(Keyframe{TrackID: 1, SampleNumber: 1, SegmentIndex: 0})
	err := idx.Finalize()
	se, ok := errs.AsError(err)
	if !ok || se.Kind != errs.KindIndex {
		t.Fatalf("want KindIndex duplicate error, got %v", err)
	}
}

func TestInvalidKeyframe(t *testing.T) {
	idx := New()
	if err := idx.Add(Keyframe{TrackID: 0, SampleNumber: 1}); err == nil {
		t.Fatal("zero track id must error")
	}
	if err := idx.Add(Keyframe{TrackID: 1, SampleNumber: 0}); err == nil {
		t.Fatal("zero sample number must error")
	}
}

func TestRejectKeepsObjectOutOfIndex(t *testing.T) {
	idx := New()
	idx.Reject("seg-corrupt-3")
	if !idx.IsRejected("seg-corrupt-3") {
		t.Fatal("rejected object must be marked")
	}
	if idx.IsRejected("seg-good-1") {
		t.Fatal("unrelated object must not be rejected")
	}
}

func TestHitRateUnknownCountsAsMiss(t *testing.T) {
	idx := New()
	// Boundary (1,0) has a known keyframe; (1,1) only an unknown one.
	_ = idx.Add(Keyframe{TrackID: 1, SampleNumber: 1, SegmentIndex: 0, State: StateKnown})
	_ = idx.Add(Keyframe{TrackID: 1, SampleNumber: 30, SegmentIndex: 1, State: StateUnknown})
	wanted := []Boundary{{1, 0}, {1, 1}}
	if got := idx.HitRate(wanted); got != 0.5 {
		t.Fatalf("hit rate = %v, want 0.5", got)
	}
	if idx.KnownCount() != 1 || idx.UnknownCount() != 1 || idx.Len() != 2 {
		t.Fatalf("counts known=%d unknown=%d len=%d",
			idx.KnownCount(), idx.UnknownCount(), idx.Len())
	}
}

func TestRebuildPreservesUnknown(t *testing.T) {
	records := []Keyframe{
		{TrackID: 1, SampleNumber: 9, SegmentIndex: 2, State: StateUnknown, ByteOffset: 77},
		{TrackID: 1, SampleNumber: 1, SegmentIndex: 0, State: StateKnown, ByteOffset: 11},
	}
	idx, err := Rebuild(records)
	if err != nil {
		t.Fatal(err)
	}
	snap := idx.Snapshot()
	if snap[0].SampleNumber != 1 || snap[1].State != StateUnknown {
		t.Fatalf("rebuild reordered or promoted unknown: %+v", snap)
	}
	// A garbage state is rejected rather than silently normalized.
	if _, err := Rebuild([]Keyframe{{TrackID: 1, SampleNumber: 1, State: "guessed"}}); err == nil {
		t.Fatal("invalid keyframe state must be rejected")
	}
}

func TestHitRateEmpty(t *testing.T) {
	idx := New()
	if idx.HitRate(nil) != 0 {
		t.Fatal("empty wanted set must report 0, not divide by zero")
	}
}
