package overlap

import (
	"testing"

	"subtitle/internal/timebase"
)

func items() []Item {
	return []Item{
		{Index: 1, Frames: timebase.Frames{Start: 0, End: 24}},
		{Index: 2, Frames: timebase.Frames{Start: 24, End: 48}}, // touching, no overlap
		{Index: 3, Frames: timebase.Frames{Start: 36, End: 60}}, // overlaps cue 2
		{Index: 4, Frames: timebase.Frames{Start: 36, End: 40}}, // same start as cue 3, fully inside
	}
}

func TestKeepPreservesEverything(t *testing.T) {
	oc, err := Resolve(items(), Keep)
	if err != nil {
		t.Fatal(err)
	}
	if Dropped(oc) != 0 || len(oc) != 4 {
		t.Fatalf("keep must not drop anything: %+v", oc)
	}
	if oc[2].Frames.Start != 36 {
		t.Fatalf("keep must not move frames: %+v", oc[2])
	}
}

func TestTrimShiftsStart(t *testing.T) {
	oc, err := Resolve(items(), Trim)
	if err != nil {
		t.Fatal(err)
	}
	// Cue 3 starts at 36 but cue 2 ends at 48: trimmed to [48,60).
	if oc[2].Dropped || oc[2].Frames.Start != 48 || oc[2].Frames.End != 60 {
		t.Fatalf("cue3 trim: %+v", oc[2])
	}
	// Cue 4 [36,40) collided with cue 3 before trimming, but cue 3 has now
	// moved to [48,60), so the intervals no longer intersect and cue 4 lives.
	if oc[3].Dropped {
		t.Fatalf("cue4 no longer overlaps after trim: %+v", oc[3])
	}
	if Dropped(oc) != 0 {
		t.Fatalf("dropped count = %d", Dropped(oc))
	}
}

func TestDropLater(t *testing.T) {
	oc, err := Resolve(items(), DropLater)
	if err != nil {
		t.Fatal(err)
	}
	// Cue 3 overlaps surviving cue 2 -> dropped; cue 4 then overlaps cue 2
	// (its start 36 is still before cue 2's end 48) -> dropped as well.
	if !oc[2].Dropped || !oc[3].Dropped {
		t.Fatalf("both later overlapping cues drop: %+v", oc)
	}
	// Stable source order is reflected in reported indices and reasons.
	if oc[2].Index != 3 || oc[2].Reason == "" || oc[3].Index != 4 {
		t.Fatalf("ordering wrong: %+v", oc)
	}
	if Dropped(oc) != 2 {
		t.Fatalf("dropped count = %d", Dropped(oc))
	}
}

func TestSubFrameTrimKeptUnderKeep(t *testing.T) {
	in := []Item{
		{Index: 1, Frames: timebase.Frames{Start: 0, End: 10}},
		{Index: 2, Frames: timebase.Frames{Start: 9, End: 10}},
	}
	oc, err := Resolve(in, Trim)
	if err != nil {
		t.Fatal(err)
	}
	if !oc[1].Dropped {
		t.Fatalf("one-frame overlap trims to nothing: %+v", oc[1])
	}
	oc, _ = Resolve(in, Keep)
	if Dropped(oc) != 0 {
		t.Fatal("keep never drops")
	}
}

func TestSameStartStableOrder(t *testing.T) {
	in := []Item{
		{Index: 1, Frames: timebase.Frames{Start: 100, End: 120}},
		{Index: 2, Frames: timebase.Frames{Start: 100, End: 110}},
	}
	oc, err := Resolve(in, DropLater)
	if err != nil {
		t.Fatal(err)
	}
	if oc[0].Dropped || !oc[1].Dropped {
		t.Fatalf("earlier same-start cue must win: %+v", oc)
	}
}

func TestInvalidStrategy(t *testing.T) {
	if _, err := Resolve(items(), "merge"); err == nil {
		t.Fatal("expected strategy validation error")
	}
}
