package index

import (
	"testing"

	"mediad/internal/clock"
	"mediad/internal/ebml"
)

func te(track uint64, tick int64, key bool, off int64, ns int64) TimedEvent {
	return TimedEvent{
		Event:  ebml.Event{Track: track, Tick: tick, Keyframe: key, Offset: off, Size: 16},
		TimeNs: ns,
	}
}

func sampleIndex() (*Index, []TimedEvent) {
	events := []TimedEvent{
		te(1, 0, true, 100, 0),
		te(2, 0, false, 120, 0),
		te(1, 40, false, 140, 40_000_000),
		te(1, 80, true, 200, 80_000_000),
		te(2, 120, false, 220, 120_000_000),
	}
	return Build(events, 500, 160_000_000), events
}

func TestBuildOnlyKeyframes(t *testing.T) {
	ix, _ := sampleIndex()
	if len(ix.Keys) != 2 {
		t.Fatalf("got %d keys, want 2", len(ix.Keys))
	}
	if ix.Keys[0].Offset != 100 || ix.Keys[1].Offset != 200 {
		t.Fatalf("bad key offsets: %+v", ix.Keys)
	}
}

func TestQueryClosedOpen(t *testing.T) {
	ix, events := sampleIndex()
	seg, err := ix.Query(events, 0, 80_000_000)
	if err != nil {
		t.Fatal(err)
	}
	// [0,80ms): events at 0,0,40ms; source extent from keyframe@100 to
	// keyframe@200 (the keyframe at 80ms opens the next segment).
	if len(seg.Events) != 3 {
		t.Fatalf("got %d events, want 3", len(seg.Events))
	}
	if seg.SrcOffset != 100 || seg.SrcLength != 100 {
		t.Fatalf("src extent = [%d,%d), want [100,200)",
			seg.SrcOffset, seg.SrcOffset+seg.SrcLength)
	}
	// Second interval starts exactly on the keyframe.
	seg2, err := ix.Query(events, 80_000_000, 160_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if seg2.SrcOffset != 200 || seg2.SrcOffset+seg2.SrcLength != 500 {
		t.Fatalf("second extent = [%d,%d)", seg2.SrcOffset, seg2.SrcOffset+seg2.SrcLength)
	}
	if len(seg2.Events) != 2 {
		t.Fatalf("got %d events in second interval, want 2", len(seg2.Events))
	}
}

func TestQueryRejectsOutOfBounds(t *testing.T) {
	ix, events := sampleIndex()
	cases := [][2]int64{
		{-1, 10},                   // negative start
		{10, 10},                   // empty
		{20, 10},                   // inverted
		{0, 200_000_000},           // end beyond duration
		{160_000_000, 170_000_000}, // start at/after duration
	}
	for _, c := range cases {
		if _, err := ix.Query(events, c[0], c[1]); clock.CodeOf(err) != clock.ErrBounds {
			t.Fatalf("interval %v: want ERR_BOUNDS, got %v", c, err)
		}
	}
}

func TestQueryMidGOPStartsAtKeyframe(t *testing.T) {
	ix, events := sampleIndex()
	// Start inside the first GOP: source must begin at keyframe offset 100.
	seg, err := ix.Query(events, 40_000_000, 120_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if seg.SrcOffset != 100 {
		t.Fatalf("src offset = %d, want 100", seg.SrcOffset)
	}
	if len(seg.Events) != 2 { // events at 40ms and 80ms
		t.Fatalf("got %d events, want 2", len(seg.Events))
	}
}

func TestHasKeyframeAt(t *testing.T) {
	ix, _ := sampleIndex()
	if !ix.HasKeyframeAt(0) || !ix.HasKeyframeAt(80_000_000) {
		t.Fatal("missing expected keyframes")
	}
	if ix.HasKeyframeAt(40_000_000) {
		t.Fatal("40ms is not a keyframe")
	}
}
