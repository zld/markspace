package overlap

import (
	"testing"
	"time"

	"subsync/internal/model"
)

func cue(id, track string, order int, start, end time.Duration) model.Cue {
	return model.Cue{ID: id, Track: track, Order: order, Start: start, End: end}
}

func TestKeep(t *testing.T) {
	cues := []model.Cue{
		cue("1", "a", 0, 0, 10*time.Second),
		cue("2", "a", 1, 5*time.Second, 15*time.Second),
	}
	res, err := Resolve(cues, Keep)
	if err != nil {
		t.Fatal(err)
	}
	if res.Overlaps != 1 || res.Dropped != 0 || len(res.Cues) != 2 {
		t.Errorf("keep = overlaps %d dropped %d kept %d", res.Overlaps, res.Dropped, len(res.Cues))
	}
	if res.Cues[1].Start != 5*time.Second {
		t.Error("keep must not mutate cue timing")
	}
}

func TestClip(t *testing.T) {
	cues := []model.Cue{
		cue("1", "a", 0, 0, 10*time.Second),
		cue("2", "a", 1, 5*time.Second, 15*time.Second),
		cue("3", "a", 2, 12*time.Second, 14*time.Second), // fully inside cue 2 -> zero length
	}
	res, err := Resolve(cues, Clip)
	if err != nil {
		t.Fatal(err)
	}
	if res.Overlaps != 2 {
		t.Errorf("overlaps = %d, want 2", res.Overlaps)
	}
	if res.Dropped != 1 {
		t.Errorf("dropped = %d, want 1", res.Dropped)
	}
	if len(res.Cues) != 2 {
		t.Fatalf("kept %d cues, want 2", len(res.Cues))
	}
	if res.Cues[1].Start != 10*time.Second || res.Cues[1].End != 15*time.Second {
		t.Errorf("clipped cue timing wrong: %s..%s", res.Cues[1].Start, res.Cues[1].End)
	}
}

func TestClipExactBoundary(t *testing.T) {
	// Touching but not intersecting (start == prior end) is not an overlap.
	cues := []model.Cue{
		cue("1", "a", 0, 0, 10*time.Second),
		cue("2", "a", 1, 10*time.Second, 20*time.Second),
	}
	res, err := Resolve(cues, Clip)
	if err != nil {
		t.Fatal(err)
	}
	if res.Overlaps != 0 || res.Dropped != 0 {
		t.Errorf("boundary-touch counted as overlap: %+v", res)
	}
}

func TestFail(t *testing.T) {
	cues := []model.Cue{
		cue("1", "a", 0, 0, 10*time.Second),
		cue("2", "a", 1, 9*time.Second, 12*time.Second),
	}
	if _, err := Resolve(cues, Fail); err == nil {
		t.Error("fail strategy expected an error on overlap")
	}
}

func TestTracksIndependent(t *testing.T) {
	cues := []model.Cue{
		cue("1", "a", 0, 0, 10*time.Second),
		cue("2", "b", 0, 0, 10*time.Second),
		cue("3", "b", 1, 5*time.Second, 12*time.Second), // overlaps cue 2, still positive
	}
	// Clip never errors: track a's single cue and track b's two cues are
	// resolved separately, and only track b contains an overlap.
	res, err := Resolve(cues, Clip)
	if err != nil {
		t.Fatalf("different tracks must not collide: %v", err)
	}
	if res.Overlaps != 1 {
		t.Errorf("overlaps = %d, want 1 (only within track b)", res.Overlaps)
	}
	if len(res.Cues) != 3 {
		t.Errorf("cross-track clipping dropped cues: %d remain", len(res.Cues))
	}
}

func TestParseStrategyErrors(t *testing.T) {
	if _, err := ParseStrategy("nope"); err == nil {
		t.Error("expected error for unknown strategy")
	}
}
