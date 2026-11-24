package subtitle

import (
	"testing"

	"mediaaudit/internal/model"
)

func mkCue(idx int, start, end model.Rat, line int) Cue {
	return Cue{Index: idx, Start: start, End: end, StartText: "s", EndText: "e",
		Lines: []string{"t"}, Pos: model.InputPos{File: "f.srt", Line: line}}
}

func TestSplitAtCutsTilesExactly(t *testing.T) {
	cue := mkCue(1, model.RatFromInt(0), model.RatFromInt(4), 2)
	cuts := []model.Rat{
		model.MustRat(1, 2), model.RatFromInt(2), model.RatFromInt(4),
		model.RatFromInt(0), model.MustRat(1, 2), model.RatFromInt(10),
	}
	// Cuts outside [start,end) and duplicates must be ignored.
	pieces, err := SplitAtCuts(cue, cuts)
	if err != nil {
		t.Fatal(err)
	}
	if len(pieces) != 3 {
		t.Fatalf("pieces=%d", len(pieces))
	}
	wantStart := []model.Rat{model.RatFromInt(0), model.MustRat(1, 2), model.RatFromInt(2)}
	wantEnd := []model.Rat{model.MustRat(1, 2), model.RatFromInt(2), model.RatFromInt(4)}
	for i := range pieces {
		if !pieces[i].Start.Equal(wantStart[i]) || !pieces[i].End.Equal(wantEnd[i]) {
			t.Fatalf("piece %d = %s..%s", i, pieces[i].Start, pieces[i].End)
		}
		if pieces[i].Text() != cue.Text() {
			t.Fatalf("piece %d lost text", i)
		}
	}
}

func TestSplitAtCutsNoCuts(t *testing.T) {
	cue := mkCue(1, model.RatFromInt(1), model.RatFromInt(2), 1)
	pieces, err := SplitAtCuts(cue, []model.Rat{model.RatFromInt(0), model.RatFromInt(5)})
	if err != nil || len(pieces) != 1 {
		t.Fatalf("pieces=%v err=%v", pieces, err)
	}
}

func TestSplitAtCutsReversed(t *testing.T) {
	cue := mkCue(1, model.RatFromInt(3), model.RatFromInt(2), 1)
	if _, err := SplitAtCuts(cue, nil); err == nil {
		t.Fatal("reversed cue must be rejected")
	}
}

func TestNormalizeIsDeterministic(t *testing.T) {
	a := mkCue(3, model.RatFromInt(0), model.RatFromInt(2), 1)
	b := mkCue(1, model.RatFromInt(0), model.RatFromInt(2), 2)
	c := mkCue(2, model.RatFromInt(1), model.RatFromInt(3), 3)
	out := Normalize([]Cue{a, b, c})
	if out[0].Index != 1 || out[1].Index != 3 || out[2].Index != 2 {
		t.Fatalf("order %d %d %d", out[0].Index, out[1].Index, out[2].Index)
	}
	if a.Index != 3 {
		t.Fatal("Normalize must not mutate input")
	}
	again := Normalize([]Cue{a, b, c})
	for i := range out {
		if out[i].Index != again[i].Index {
			t.Fatal("ordering not stable")
		}
	}
}

func TestCutPointsDedup(t *testing.T) {
	got := CutPointsFromBoundaries([]model.Rat{
		model.MustRat(1, 2), model.MustRat(1, 3), model.MustRat(2, 6), model.RatFromInt(1),
	})
	if len(got) != 3 {
		t.Fatalf("dedup %v", got)
	}
	if !got[0].Equal(model.MustRat(1, 2)) || !got[1].Equal(model.MustRat(1, 3)) || !got[2].Equal(model.RatFromInt(1)) {
		t.Fatalf("order/values %v", got)
	}
}

func TestOverlapNoFalsePositiveForTouchingCues(t *testing.T) {
	a := mkCue(1, model.RatFromInt(0), model.RatFromInt(2), 1)
	b := mkCue(2, model.RatFromInt(2), model.RatFromInt(3), 4)
	ov, errs := Overlaps([]Cue{a, b})
	if len(ov) != 0 || len(errs) != 0 {
		t.Fatalf("touching cues must not overlap: %v %v", ov, errs)
	}
}
