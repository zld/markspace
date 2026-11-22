package clock

import (
	"errors"
	"math"
	"testing"
)

func TestExtendWrapAndSameTimestamp(t *testing.T) {
	e := NewExtender(1000)
	if _, err := e.Extend(0xFFFFFFF0); err != nil {
		t.Fatal(err)
	}
	t1, _ := e.Extend(0xFFFFFFF0) // same timestamp collapses
	if t1 != 0xFFFFFFF0 {
		t.Fatalf("same timestamp moved: %d", t1)
	}
	t2, err := e.Extend(10) // wrapped forward by 26 ticks
	if err != nil {
		t.Fatal(err)
	}
	if t2 != 0xFFFFFFF0+26 {
		t.Fatalf("wrap: want %d, got %d", 0xFFFFFFF0+26, t2)
	}
	if e.Wraps != 1 {
		t.Fatalf("want 1 wrap, got %d", e.Wraps)
	}
}

func TestExtendBackwardClampAndReject(t *testing.T) {
	e := NewExtender(100)
	if _, err := e.Extend(5000); err != nil {
		t.Fatal(err)
	}
	t1, err := e.Extend(4950) // small backward jitter: clamped
	if err != nil {
		t.Fatal(err)
	}
	if t1 != 5000 {
		t.Fatalf("clamped tick moved: %d", t1)
	}
	if e.Clamped != 1 {
		t.Fatalf("want 1 clamp, got %d", e.Clamped)
	}
	if _, err := e.Extend(1000); !errors.Is(err, ErrNegativeAdvance) {
		t.Fatalf("want ErrNegativeAdvance, got %v", err)
	}
}

func TestExtendLateHistorical(t *testing.T) {
	e := NewExtender(1 << 20)
	for _, ts := range []uint32{1000, 2000, 3000} {
		if _, err := e.Extend(ts); err != nil {
			t.Fatal(err)
		}
	}
	got, err := e.ExtendLate(1500) // belongs between 1000 and 2000
	if err != nil {
		t.Fatal(err)
	}
	if got != 1500 {
		t.Fatalf("historical tick = %d, want 1500", got)
	}
	// State undisturbed: next on-time extend still works.
	t4, _ := e.Extend(4000)
	if t4 != 4000 {
		t.Fatalf("state disturbed: %d", t4)
	}
}

func TestTicksMillisOverflow(t *testing.T) {
	if _, err := TicksToMillis(math.MaxInt64, 90000); err == nil {
		t.Fatal("want overflow error")
	}
	if _, err := MillisToTicks(math.MaxInt64, 90000); err == nil {
		t.Fatal("want overflow error")
	}
	ms, err := TicksToMillis(90000*1000, 90000)
	if err != nil || ms != 1000000 {
		t.Fatalf("90000*1000 ticks = %d ms (%v), want 1000000", ms, err)
	}
	if _, err := TicksToMillis(1, 0); err == nil {
		t.Fatal("want zero-rate error")
	}
}

func TestWatermarkMonotonic(t *testing.T) {
	w := NewWatermark(100)
	if w.Closed(0) {
		t.Fatal("nothing closed before any event")
	}
	w.Observe(500)
	if w.Current() != 400 {
		t.Fatalf("watermark = %d, want 400", w.Current())
	}
	w.Observe(300) // behind: watermark must not retreat
	if w.Current() != 400 {
		t.Fatalf("watermark retreated to %d", w.Current())
	}
	if !w.Closed(400) || w.Closed(401) {
		t.Fatalf("closed boundary wrong at %d", w.Current())
	}
}

func rec(t int64, seq int64, b byte) EventRecord {
	var h [32]byte
	h[0] = b
	return EventRecord{Time: t, ExtSeq: seq, Hash: h}
}

func TestReplayLateRecomputeDeterministic(t *testing.T) {
	r := NewReplayer(100, 8)
	r.Add(rec(10, 1, 0xA1), 0)
	r.Add(rec(20, 2, 0xA2), 0)
	closed := r.Add(rec(110, 3, 0xB1), 101) // closes window 0
	if len(closed) != 1 || closed[0].ID != 0 {
		t.Fatalf("want window 0 closed, got %+v", closed)
	}
	before := closed[0].Hash

	w, ok := r.AddLate(rec(30, 4, 0xA3)) // late into closed window 0
	if !ok {
		t.Fatal("late event rejected")
	}
	if w.Hash == before {
		t.Fatal("late event did not change the affected window hash")
	}
	if w.Late != 1 {
		t.Fatalf("late count = %d, want 1", w.Late)
	}

	// Rebuilding the same window from scratch in any arrival order must
	// produce the same hash.
	r2 := NewReplayer(100, 8)
	r2.Add(rec(30, 4, 0xA3), 0)
	r2.Add(rec(10, 1, 0xA1), 0)
	r2.Add(rec(20, 2, 0xA2), 0)
	got := r2.Flush()
	if got[0].Hash != w.Hash {
		t.Fatalf("non-deterministic replay: %x != %x", got[0].Hash[:8], w.Hash[:8])
	}
}

func TestReplayUnaffectedWindowsStable(t *testing.T) {
	r := NewReplayer(100, 8)
	r.Add(rec(10, 1, 0xA1), 0)
	r.Add(rec(110, 2, 0xB1), 0)
	closed := r.Add(rec(210, 3, 0xC1), 201) // closes windows 0 and 1
	if len(closed) != 2 {
		t.Fatalf("want 2 closed windows, got %d", len(closed))
	}
	w1before := closed[1].Hash
	r.AddLate(rec(20, 3, 0xA2)) // touches window 0 only
	wins := r.Flush()
	for _, w := range wins {
		if w.ID == 1 && w.Hash != w1before {
			t.Fatal("unaffected window hash changed by late replay")
		}
	}
}

func TestReplayEvictionDropsLate(t *testing.T) {
	r := NewReplayer(100, 2)
	r.Add(rec(10, 1, 0xA1), 0)
	r.Add(rec(110, 2, 0xB1), 0)
	r.Add(rec(210, 3, 0xC1), 0) // evicts window 0
	if _, ok := r.AddLate(rec(20, 4, 0xA2)); ok {
		t.Fatal("late event for evicted window accepted")
	}
	if r.LateDropped != 1 {
		t.Fatalf("LateDropped = %d, want 1", r.LateDropped)
	}
	if len(r.Evicted) != 1 || r.Evicted[0].ID != 0 {
		t.Fatalf("evicted windows = %+v", r.Evicted)
	}
}
