package timeline

import (
	"testing"

	"mediad/internal/model"
)

func ev(ms int64, seq uint64) *model.Event {
	return &model.Event{Kind: model.KindPacket, EventMs: ms, Seq: seq}
}

func TestWatermarkMonotone(t *testing.T) {
	w := NewWatermark(100, 250)
	w.Advance(500)
	w.Advance(400)
	if w.Current() != 500 {
		t.Fatalf("watermark = %d, want 500 (must not retreat)", w.Current())
	}
}

func TestWatermarkPhases(t *testing.T) {
	w := NewWatermark(100, 250)
	if w.Emittable(0) {
		t.Fatal("nothing is emittable before the watermark starts")
	}
	w.Advance(500)
	// Presentation 400 + depth 100 = 500 is due; 401 is not.
	if !w.Emittable(400) {
		t.Fatal("event at the deadline should be emittable")
	}
	if w.Emittable(401) {
		t.Fatal("event past the deadline is not yet emittable")
	}
	// Arriving at 500, an event whose deadline was 300 is 200ms late:
	// inside the 250ms window.
	if !w.InLateWindow(200) || w.LateBy(200) != 200 {
		t.Fatalf("late window misclassified: late=%d", w.LateBy(200))
	}
	// Deadline 100 is 400ms late: outside the window.
	if w.InLateWindow(0) {
		t.Fatal("event 400ms late should be outside the 250ms window")
	}
	if got, want := w.FinalizedAt(400), int64(750); got != want {
		t.Fatalf("finalized at %d, want %d", got, want)
	}
}

func TestReplayAppendAndInsertSorted(t *testing.T) {
	r := NewReplay()
	r.Append(ev(100, 1))
	r.Append(ev(300, 2))
	r.InsertSorted(ev(200, 5))
	got := r.Outputs()
	if len(got) != 3 {
		t.Fatalf("outputs = %d, want 3", len(got))
	}
	for i, want := range []int64{100, 200, 300} {
		if got[i].EventMs != want {
			t.Fatalf("output[%d] = %d, want %d", i, got[i].EventMs, want)
		}
	}
}

func TestReplayLateWindowAndDrop(t *testing.T) {
	w := NewWatermark(100, 250)
	w.Advance(500)
	r := NewReplay()
	r.Append(ev(50, 1))

	// Deadline 300 (EventMs 200 + depth 100) is 200ms late: replay.
	if !r.InsertLate(ev(200, 9), w) {
		t.Fatal("event inside the late window should be replayed")
	}
	if r.LateReplayed != 1 || r.LateDropped != 0 {
		t.Fatalf("replay=%d drop=%d", r.LateReplayed, r.LateDropped)
	}
	// EventMs 10 has deadline 110, 390ms late: dropped.
	if r.InsertLate(ev(10, 10), w) {
		t.Fatal("event outside the late window should be dropped")
	}
	if r.LateDropped != 1 {
		t.Fatalf("late dropped = %d, want 1", r.LateDropped)
	}
}

func TestReplayDeterministic(t *testing.T) {
	build := func() []*model.Event {
		w := NewWatermark(100, 250)
		w.Advance(500)
		r := NewReplay()
		for _, e := range []*model.Event{ev(100, 1), ev(400, 4), ev(300, 3)} {
			r.Append(e)
		}
		r.InsertLate(ev(200, 2), w)
		r.InsertLate(ev(250, 6), w)
		return r.Outputs()
	}
	a, b := build(), build()
	if len(a) != len(b) {
		t.Fatalf("length mismatch: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i].EventMs != b[i].EventMs || a[i].Seq != b[i].Seq {
			t.Fatalf("output[%d] differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}
