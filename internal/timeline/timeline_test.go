package timeline

import (
	"testing"

	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
)

func ns(n int64) clock.Ticks { return clock.Nanoseconds(n) }

func TestWatermarkAdvanceAndLateReplay(t *testing.T) {
	wm := New(Config{LateWindow: ns(250)})
	mk := func(seq uint64, t int64) Event {
		return Event{Seq: seq, Time: ns(t), Keyframe: seq%3 == 0}
	}
	// In-order advances.
	for i, at := range []int64{100, 200, 300, 400} {
		d, err := wm.Push(mk(wm.NextSeq(), at))
		if err != nil || d != DecisionAdvance {
			t.Fatalf("event %d: decision=%d err=%v", i, d, err)
		}
	}
	if wm.Level() != ns(400) {
		t.Fatalf("level = %d", wm.Level())
	}
	// Late but inside the window: deterministic replay insertion.
	d, err := wm.Push(mk(wm.NextSeq(), 250))
	if err != nil || d != DecisionReplay {
		t.Fatalf("late event decision=%d err=%v", d, err)
	}
	if wm.ReplayCount() != 1 {
		t.Fatalf("replay count = %d", wm.ReplayCount())
	}
	evs := wm.Events()
	if evs[2].Time != ns(250) || evs[3].Time != ns(300) {
		t.Fatalf("late event not inserted in canonical order: %+v", evs)
	}
	// Late beyond the window is rejected.
	_, err = wm.Push(mk(wm.NextSeq(), 1))
	if errs.Class(err) != errs.KindSealed || len(wm.Rejected()) != 1 {
		t.Fatalf("want sealed rejection, got %v, rejected=%d", err, len(wm.Rejected()))
	}
}

func TestSealedPrefixRefusesLateData(t *testing.T) {
	wm := New(Config{LateWindow: ns(10_000)})
	wm.Push(Event{Seq: wm.NextSeq(), Time: ns(1000)})
	wm.Push(Event{Seq: wm.NextSeq(), Time: ns(2000)})
	if err := wm.Seal(ns(1500)); err != nil {
		t.Fatal(err)
	}
	// Even a tiny late event inside the window must not reopen the seal.
	_, err := wm.Push(Event{Seq: wm.NextSeq(), Time: ns(1200)})
	if errs.Class(err) != errs.KindSealed {
		t.Fatalf("want KindSealed, got %v", err)
	}
	if err := wm.Seal(ns(1000)); errs.Class(err) != errs.KindInternal {
		t.Fatalf("backward seal must be internal error, got %v", err)
	}
}

func TestReplayDeterminismAndSealProtection(t *testing.T) {
	// Pure builder: one boundary per keyframe event.
	builder := func(events []Event) ([]Boundary, error) {
		var out []Boundary
		for _, e := range events {
			if e.Keyframe {
				out = append(out, Boundary{Index: len(out), Time: e.Time})
			}
		}
		return out, nil
	}
	wm := New(Config{LateWindow: ns(5000)})
	r := NewReplayer(wm, builder)
	for _, at := range []int64{100, 200, 300, 400, 500} {
		wm.Push(Event{Seq: wm.NextSeq(), Time: ns(at), Keyframe: at%200 == 0})
	}
	first, err := r.Rebuild()
	if err != nil || len(first) != 2 {
		t.Fatalf("initial rebuild: %d boundaries err=%v", len(first), err)
	}
	// Seal the first boundary.
	if err := wm.Seal(ns(200)); err != nil {
		t.Fatal(err)
	}
	// Late keyframe inside the window, after the seal.
	wm.Push(Event{Seq: wm.NextSeq(), Time: ns(350), Keyframe: true})
	second, err := r.Rebuild()
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 3 {
		t.Fatalf("replay should add one boundary, got %d", len(second))
	}
	if second[0].Time != ns(200) || second[0].Generation != 1 {
		t.Fatalf("sealed boundary mutated: %+v", second[0])
	}
	if r.Generation() != 2 || wm.ReplayCount() != 1 {
		t.Fatalf("generation=%d replays=%d", r.Generation(), wm.ReplayCount())
	}
	// Repeated rebuild from identical state yields identical output.
	third, _ := r.Rebuild()
	if len(third) != len(second) {
		t.Fatal("non-deterministic rebuild length")
	}
	for i := range second {
		if second[i] != third[i] {
			t.Fatalf("boundary %d differs across rebuilds", i)
		}
	}
}

func TestUntrustedIntervalsMerged(t *testing.T) {
	wm := New(Config{LateWindow: ns(1000)})
	if err := wm.AddInterval(Interval{Start: ns(100), End: ns(300), Reason: "burst-loss"}); err != nil {
		t.Fatal(err)
	}
	if err := wm.AddInterval(Interval{Start: ns(200), End: ns(400), Reason: "subtitle-boundary"}); err != nil {
		t.Fatal(err)
	}
	got := wm.Untrusted()
	if len(got) != 1 || got[0].Start != ns(100) || got[0].End != ns(400) {
		t.Fatalf("intervals not merged: %+v", got)
	}
	if err := wm.AddInterval(Interval{Start: ns(9), End: ns(1)}); errs.Class(err) != errs.KindInvalidInput {
		t.Fatalf("want invalid_input for bad interval, got %v", err)
	}
}
