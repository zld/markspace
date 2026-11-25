package media

import (
	"testing"

	mp4 "github.com/abema/go-mp4"
)

func TestTickCursorVariableDeltas(t *testing.T) {
	stts := &mp4.Stts{Entries: []mp4.SttsEntry{
		{SampleCount: 3, SampleDelta: 1000},
		{SampleCount: 2, SampleDelta: 5000},
		{SampleCount: 2, SampleDelta: 1000},
	}}
	cur := newTickCursor(stts)
	want := []struct {
		start uint64
		delta uint32
	}{
		{0, 1000}, {1000, 1000}, {2000, 1000},
		{3000, 5000}, {8000, 5000},
		{13000, 1000}, {14000, 1000},
	}
	for i, w := range want {
		start, delta, err := cur.next()
		if err != nil {
			t.Fatalf("sample %d: %v", i, err)
		}
		if start != w.start || delta != w.delta {
			t.Fatalf("sample %d = (%d,%d) want (%d,%d)", i, start, delta, w.start, w.delta)
		}
	}
	if _, _, err := cur.next(); err == nil {
		t.Fatal("expected truncation after last sample")
	} else if !IsKind(err, KindTruncatedInput) {
		t.Fatalf("kind=%v", Category(err))
	}
}

func TestTickCursorRejectsOverflow(t *testing.T) {
	// Accumulated ticks are guarded against the safe domain. Drive the cursor
	// directly to a near-limit position by priming its state, avoiding a
	// multi-billion-sample loop.
	stts := &mp4.Stts{Entries: []mp4.SttsEntry{{SampleCount: 2, SampleDelta: 0xFFFFFFFF}}}
	cur := newTickCursor(stts)
	cur.nextTick = 1<<62 - 1 // one tick below the domain ceiling
	if _, _, err := cur.next(); err != nil {
		// The first max-delta step crosses the ceiling.
		if !IsKind(err, KindLimit) {
			t.Fatalf("kind=%v", Category(err))
		}
		return
	}
	t.Fatal("expected domain overflow")
}

func TestTickCursorMaxDeltaWithinDomain(t *testing.T) {
	// A single max-delta sample is well inside the domain and must succeed.
	stts := &mp4.Stts{Entries: []mp4.SttsEntry{{SampleCount: 1, SampleDelta: 0xFFFFFFFF}}}
	cur := newTickCursor(stts)
	start, delta, err := cur.next()
	if err != nil {
		t.Fatal(err)
	}
	if start != 0 || delta != 0xFFFFFFFF {
		t.Fatalf("got (%d,%d)", start, delta)
	}
}
