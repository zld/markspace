package timeline

import (
	"testing"

	"mediad/internal/model"
)

func sampleEvents() []Event {
	return []Event{
		{ID: "a", TimeNS: 1_000_000, Track: 1, Payload: []byte("alpha")},
		{ID: "b", TimeNS: 2_000_000, Track: 1, Payload: []byte("bravo")},
		{ID: "c", TimeNS: 3_000_000, Track: 2, Payload: []byte("charlie")},
		{ID: "d", TimeNS: 4_000_000, Track: 2, Payload: []byte("delta")},
	}
}

func run(events []Event, windowNS int64) (Snapshot, error) {
	eng, err := NewEngine(EngineConfig{LateWindowNS: windowNS})
	if err != nil {
		return Snapshot{}, err
	}
	var firstErr error
	for _, ev := range events {
		if err := eng.Push(ev); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return eng.Snapshot(), firstErr
}

func TestOrderIndependence(t *testing.T) {
	base := sampleEvents()

	// In-order run.
	s1, err := run(base, 5_000_000)
	if err != nil {
		t.Fatal(err)
	}

	// Scrambled arrivals, including a late event inside the window.
	shuffled := []Event{base[3], base[0], base[2], base[1]}
	s2, err := run(shuffled, 5_000_000)
	if err != nil {
		t.Fatal(err)
	}

	if s1.TimelineHash != s2.TimelineHash {
		t.Fatalf("hash differs by arrival order:\n%+v\n%+v", s1, s2)
	}
	if len(s1.Intervals) != len(s2.Intervals) || len(s1.Intervals) != 4 {
		t.Fatalf("interval count wrong: %d vs %d", len(s1.Intervals), len(s2.Intervals))
	}
	if s1.Watermark != s2.Watermark || s1.Watermark != 4_000_000 {
		t.Fatalf("watermark mismatch: %d vs %d", s1.Watermark, s2.Watermark)
	}
}

func TestLateInsideWindowRecomputes(t *testing.T) {
	eng, _ := NewEngine(EngineConfig{LateWindowNS: 2_000_000})
	ordered := sampleEvents()
	for _, ev := range ordered {
		if err := eng.Push(ev); err != nil {
			t.Fatal(err)
		}
	}
	before := eng.Snapshot()

	// An event at 2.5ms arrives last: 1.5ms behind the 4ms watermark, in window.
	late := Event{ID: "late", TimeNS: 2_500_000, Track: 1, Payload: []byte("x")}
	if err := eng.Push(late); err != nil {
		t.Fatalf("in-window late event rejected: %v", err)
	}
	after := eng.Snapshot()
	if after.Recomputed != 1 || after.Rejected != 0 {
		t.Fatalf("recompute counters wrong: %+v", after)
	}
	if after.TimelineHash == before.TimelineHash {
		t.Fatal("recompute must change the timeline hash")
	}
	// Determinism: replaying the exact sequence must reproduce the hash.
	replay, _ := run([]Event{ordered[0], ordered[1], ordered[2], ordered[3], late}, 2_000_000)
	if replay.TimelineHash != after.TimelineHash {
		t.Fatal("replayed timeline hash diverged")
	}
}

func TestLateOutsideWindowRejected(t *testing.T) {
	eng, _ := NewEngine(EngineConfig{LateWindowNS: 1_000_000})
	for _, ev := range sampleEvents() {
		if err := eng.Push(ev); err != nil {
			t.Fatal(err)
		}
	}
	tooLate := Event{ID: "old", TimeNS: 1_000_000, Track: 3, Payload: []byte("z")}
	// Watermark 4ms, event 1ms -> lag 3ms > 1ms window.
	err := eng.Push(tooLate)
	if !model.HasCode(err, model.ErrLateRejected) {
		t.Fatalf("want LATE_REJECTED, got %v", err)
	}
	me := model.AsMediaError(err)
	if me.Track != 3 || me.Offset != 1_000_000 {
		t.Fatalf("structured location lost: %+v", me)
	}
	snap := eng.Snapshot()
	if snap.Rejected != 1 || snap.Recomputed != 0 {
		t.Fatalf("reject counters wrong: %+v", snap)
	}
}

func TestBoundaryInclusiveWindow(t *testing.T) {
	eng, _ := NewEngine(EngineConfig{LateWindowNS: 1_000_000})
	_ = eng.Push(Event{ID: "a", TimeNS: 0})
	_ = eng.Push(Event{ID: "b", TimeNS: 2_000_000})
	// Lag exactly 1ms == window: must be accepted.
	err := eng.Push(Event{ID: "c", TimeNS: 1_000_000})
	if err != nil {
		t.Fatalf("window boundary must be inclusive: %v", err)
	}
	if eng.Snapshot().Recomputed != 1 {
		t.Fatal("boundary event should count as recomputed")
	}
}

func TestInvalidConfigAndStableHashes(t *testing.T) {
	if _, err := NewEngine(EngineConfig{LateWindowNS: 0}); !model.HasCode(err, model.ErrLateRejected) {
		t.Fatalf("zero window must error, got %v", err)
	}
	// Two independent engines fed identical streams must hash identically.
	s1, _ := run(sampleEvents(), 3_000_000)
	s2, _ := run(sampleEvents(), 3_000_000)
	if s1.TimelineHash != s2.TimelineHash || s1.TimelineHash == 0 {
		t.Fatal("identical streams must produce identical nonzero hashes")
	}
}
