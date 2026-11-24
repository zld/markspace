package timeline

import "testing"

// TestFreezeBoundsRetainedMemory pushes a long stream far beyond the window
// and asserts that retained events and active intervals stay bounded while
// frozen intervals accumulate.
func TestFreezeBoundsRetainedMemory(t *testing.T) {
	windowNS := int64(10_000_000) // 10 ms
	eng, err := NewEngine(EngineConfig{LateWindowNS: windowNS})
	if err != nil {
		t.Fatal(err)
	}
	// 200 events spaced 5 ms apart -> spans ~1 s, ~100 windows.
	for i := 0; i < 200; i++ {
		ts := int64(i) * 5_000_000
		if err := eng.Push(Event{ID: "ev", TimeNS: ts, Track: 1, Payload: []byte{byte(i)}}); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	if eng.FrozenCount() == 0 {
		t.Fatal("long stream must freeze intervals")
	}
	// At most a few events per window: retained must be a small constant,
	// nowhere near the 200 total accepted events.
	if got := eng.AcceptedCount(); got > 8 {
		t.Fatalf("retained events=%d not bounded to the window", got)
	}
	snap := eng.Snapshot()
	// Every accepted event time still appears either as a frozen or active
	// interval; frozen plus active counts cover the full history.
	if len(snap.Intervals)+eng.FrozenCount() != 200 {
		t.Fatalf("interval coverage lost: active=%d frozen=%d, want total 200",
			len(snap.Intervals), eng.FrozenCount())
	}
}

// TestFreezePreservesOrderIndependentHash feeds the same long event set in two
// orders and requires identical hashes even after many freeze cycles.
func TestFreezePreservesOrderIndependentHash(t *testing.T) {
	build := func(shuffle bool) uint64 {
		eng, _ := NewEngine(EngineConfig{LateWindowNS: 10_000_000})
		for i := 0; i < 60; i++ {
			idx := i
			if shuffle && i%3 == 0 && i+1 < 60 {
				idx = i + 1 // swap adjacent arrivals without changing times
			} else if shuffle && i%3 == 1 {
				idx = i - 1
			}
			ts := int64(idx) * 4_000_000
			_ = eng.Push(Event{ID: "ev", TimeNS: ts, Track: 1, Payload: []byte{byte(idx)}})
		}
		return eng.Snapshot().TimelineHash
	}
	if h1, h2 := build(false), build(true); h1 != h2 {
		t.Fatalf("hash diverges after freeze by arrival order: %d vs %d", h1, h2)
	}
}

// TestFreezeHashMatchesUnboundedReference verifies that freezing does not
// change the final hash relative to an engine whose window covers the whole
// stream (i.e. never freezes): both must hash every interval identically.
func TestFreezeHashMatchesUnboundedReference(t *testing.T) {
	events := make([]Event, 80)
	for i := range events {
		events[i] = Event{ID: "ev", TimeNS: int64(i) * 3_000_000, Track: 2, Payload: []byte("x")}
	}

	bounded, _ := NewEngine(EngineConfig{LateWindowNS: 10_000_000})
	for _, ev := range events {
		_ = bounded.Push(ev)
	}
	// A window wider than the whole span never freezes, retaining every event.
	reference, _ := NewEngine(EngineConfig{LateWindowNS: 10_000_000_000})
	for _, ev := range events {
		_ = reference.Push(ev)
	}

	bh := bounded.Snapshot().TimelineHash
	rh := reference.Snapshot().TimelineHash
	if bh != rh {
		t.Fatalf("bounded/frozen hash %d != full-history reference %d (frozen=%d)",
			bh, rh, bounded.FrozenCount())
	}
	if bounded.FrozenCount() == 0 {
		t.Fatal("test setup must actually freeze intervals")
	}
	if reference.FrozenCount() != 0 {
		t.Fatal("reference engine must not freeze")
	}
}

// TestLateEventAfterFreezeStillRecomputes confirms an in-window event arriving
// after earlier intervals froze still recomputes the active region correctly.
func TestLateEventAfterFreezeStillRecomputes(t *testing.T) {
	eng, _ := NewEngine(EngineConfig{LateWindowNS: 10_000_000})
	for i := 0; i < 10; i++ {
		_ = eng.Push(Event{ID: "ev", TimeNS: int64(i) * 5_000_000, Track: 1})
	}
	frozenBefore := eng.FrozenCount()
	// Watermark is 45ms; an event at 40ms is 5ms late, inside the 10ms window.
	late := Event{ID: "late", TimeNS: 40_000_000, Track: 1, Payload: []byte("L")}
	if err := eng.Push(late); err != nil {
		t.Fatalf("in-window late event rejected after freeze: %v", err)
	}
	if eng.Snapshot().Recomputed < 1 {
		t.Fatal("late recompute not counted")
	}
	if eng.FrozenCount() < frozenBefore {
		t.Fatal("frozen prefix must never shrink")
	}
}
