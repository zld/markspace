package timeline

import (
	"testing"

	"mediatimeline/internal/container"
)

func ev(track uint32, idx int64, dtsNS uint64) container.SampleEvent {
	return container.SampleEvent{TrackID: track, SampleIndex: idx, DTSNanos: dtsNS, PTSNanos: dtsNS}
}

func TestWatermarkMonotonicAndLate(t *testing.T) {
	w := &Watermark{}
	seq := []container.SampleEvent{
		ev(1, 0, 100), ev(1, 1, 200), ev(1, 2, 150), // third is late
		ev(1, 3, 300), ev(1, 4, 50), // also late
	}
	wantLate := []bool{false, false, true, false, true}
	for i, e := range seq {
		if got := w.Advance(e); got != wantLate[i] {
			t.Fatalf("event %d late=%v want %v", i, got, wantLate[i])
		}
	}
	if w.MarkNS() != 300 || w.LateCount() != 2 || w.TotalCount() != 5 {
		t.Fatalf("mark=%d late=%d total=%d", w.MarkNS(), w.LateCount(), w.TotalCount())
	}
	// Snapshot/restore preserves the mark exactly.
	s := w.Snapshot()
	w2 := &Watermark{}
	w2.Restore(s)
	if w2.MarkNS() != 300 || w2.LateCount() != 2 {
		t.Fatal("watermark restore diverged")
	}
}

func TestReplayDedupAndOrder(t *testing.T) {
	r := NewReplayLog()
	// Same identity arriving twice (retransmission) is admitted once.
	if !r.AddLate(ev(1, 7, 100)) {
		t.Fatal("first admission rejected")
	}
	if r.AddLate(ev(1, 7, 100)) {
		t.Fatal("duplicate admitted")
	}
	r.AddLate(ev(1, 9, 50))
	r.AddLate(ev(2, 1, 75))
	if r.Pending() != 3 || r.SkippedCount() != 1 {
		t.Fatalf("pending=%d skipped=%d", r.Pending(), r.SkippedCount())
	}
	out := r.Drain()
	// Stable key order: dts 50 (1,9), 75 (2,1), 100 (1,7).
	want := []struct {
		track uint32
		idx   int64
		dts   uint64
	}{{1, 9, 50}, {2, 1, 75}, {1, 7, 100}}
	if len(out) != len(want) {
		t.Fatalf("drained %d want %d", len(out), len(want))
	}
	for i, w := range want {
		if out[i].TrackID != w.track || out[i].SampleIndex != w.idx || out[i].DTSNanos != w.dts {
			t.Fatalf("pos %d = %+v want %+v", i, out[i], w)
		}
	}
	// Determinism: restore from snapshot into a fresh log and drain again.
	r2 := NewReplayLog()
	r2.Restore(r.Snapshot())
	// Snapshot was taken after Drain, so buffer is empty but counters persist.
	if r2.Pending() != 0 || r2.ReplayedCount() != 3 || r2.SkippedCount() != 1 {
		t.Fatalf("restore: pending=%d replayed=%d skipped=%d",
			r2.Pending(), r2.ReplayedCount(), r2.SkippedCount())
	}
}

func TestStableSortTies(t *testing.T) {
	events := []container.SampleEvent{
		ev(2, 0, 100), ev(1, 0, 100), ev(1, 1, 100), ev(1, 0, 200),
	}
	StableSort(events)
	// Equal DTS 100: track 1 before track 2; within track 1, sample order kept;
	// the pre-existing (2,0) then (1,0) insertion order must be reordered by key.
	got := [][2]int64{{int64(events[0].TrackID), events[0].SampleIndex},
		{int64(events[1].TrackID), events[1].SampleIndex},
		{int64(events[2].TrackID), events[2].SampleIndex},
		{int64(events[3].TrackID), events[3].SampleIndex}}
	want := [][2]int64{{1, 0}, {1, 1}, {2, 0}, {1, 0}}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order=%v want %v", got, want)
		}
	}
}

func TestAssertMonotonicReversal(t *testing.T) {
	good := []container.SampleEvent{ev(1, 0, 10), ev(1, 1, 10), ev(1, 2, 20)}
	if err := AssertMonotonic(good); err != nil {
		t.Fatalf("non-decreasing sequence rejected: %v", err)
	}
	bad := []container.SampleEvent{ev(1, 0, 20), ev(1, 1, 19)}
	if err := AssertMonotonic(bad); err == nil {
		t.Fatal("reversal not detected")
	}
}
