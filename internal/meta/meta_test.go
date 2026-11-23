package meta

import (
	"strings"
	"testing"
)

func TestResolveEmpty(t *testing.T) {
	if _, err := Resolve(nil); err == nil {
		t.Fatal("Resolve with no descriptors must error")
	}
}

func TestResolveRotationPriorityAndConflictLog(t *testing.T) {
	mp4Desc := &TrackDescription{
		Container: KindMP4, TrackID: 1, Width: 1280, Height: 720,
		Rotation: &Rotation{Degrees: 90},
	}
	mxfDesc := &TrackDescription{
		Container: KindMXF, TrackID: 2,
		Rotation: &Rotation{Degrees: 270},
	}
	r, err := Resolve([]*TrackDescription{mp4Desc, mxfDesc})
	if err != nil {
		t.Fatal(err)
	}
	if r.Rotation.Degrees != 270 || r.Rotation.Priority != PriorityMXF {
		t.Fatalf("MXF rotation must win, got %+v", r.Rotation)
	}
	if r.Width != 1280 || r.Height != 720 {
		t.Fatalf("dimensions not carried: %dx%d", r.Width, r.Height)
	}
	var sawConflict bool
	for _, d := range r.Decisions {
		if d.Field == "rotation" && d.Action == "conflict" &&
			strings.Contains(d.Detail, "90") && strings.Contains(d.Detail, "270") {
			sawConflict = true
		}
	}
	if !sawConflict {
		t.Fatalf("rotation conflict with both values must be recorded: %+v", r.Decisions)
	}
}

func TestResolveSameRankFirstWins(t *testing.T) {
	a := &TrackDescription{Container: KindMP4, TrackID: 1, Rotation: &Rotation{Degrees: 90}}
	b := &TrackDescription{Container: KindMP4, TrackID: 2, Rotation: &Rotation{Degrees: 180}}
	r, err := Resolve([]*TrackDescription{a, b})
	if err != nil {
		t.Fatal(err)
	}
	if r.Rotation.Degrees != 90 {
		t.Fatalf("first same-rank declaration must win, got %d", r.Rotation.Degrees)
	}
}

func TestResolveCropAndColour(t *testing.T) {
	mp4Desc := &TrackDescription{
		Container: KindMP4, TrackID: 1,
		Crop:   &Crop{X: 10, Y: 20, Width: 100, Height: 200},
		Colour: &Colour{Primaries: 1, Transfer: 1, Matrix: 1, FullRange: false},
	}
	mxfDesc := &TrackDescription{
		Container: KindMXF, TrackID: 3,
		Crop:   &Crop{X: 0, Y: 0, Width: 640, Height: 360},
		Colour: &Colour{Primaries: 9, Transfer: 1, Matrix: 9, FullRange: true},
	}
	r, err := Resolve([]*TrackDescription{mp4Desc, mxfDesc})
	if err != nil {
		t.Fatal(err)
	}
	if r.Crop.Width != 640 || r.Crop.Priority != PriorityMXF {
		t.Fatalf("MXF crop must win: %+v", r.Crop)
	}
	// Per-field colour merge: primaries/matrix from MXF, transfer stays MP4.
	if r.Colour.Primaries != 9 || r.Colour.Matrix != 9 || r.Colour.Transfer != 1 {
		t.Fatalf("per-field colour merge wrong: %+v", r.Colour)
	}
	if !r.Colour.FullRange {
		t.Fatal("full-range must be OR-merged")
	}
	fields := map[string]bool{}
	for _, d := range r.Decisions {
		fields[d.Field] = true
	}
	for _, want := range []string{"crop", "colour.primaries", "colour.transfer", "colour.matrix", "colour.fullrange"} {
		if !fields[want] {
			t.Fatalf("missing decision for %s: %+v", want, r.Decisions)
		}
	}
}

func TestResolveZeroRotationNeverOverrides(t *testing.T) {
	first := &TrackDescription{Container: KindMP4, TrackID: 1, Rotation: &Rotation{Degrees: 180}}
	second := &TrackDescription{Container: KindMXF, TrackID: 2, Rotation: &Rotation{Degrees: 0}}
	r, err := Resolve([]*TrackDescription{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if r.Rotation.Degrees != 180 {
		t.Fatalf("zero rotation must not override, got %d", r.Rotation.Degrees)
	}
}
