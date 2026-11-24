package normalize

import (
	"testing"

	"archive/internal/clock"
	"archive/internal/probe"
)

func TestEmptyCropRejected(t *testing.T) {
	tr := track(100, 100)
	tr.Crops = []probe.CropDecl{{Left: 50, Right: 50, Top: 0, Bottom: 100, Source: probe.SourceClap}}
	if _, err := Resolve(tr); clock.AsKind(err) != clock.KindOutOfRange {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestNegativeCropOriginRejected(t *testing.T) {
	tr := track(100, 100)
	tr.Crops = []probe.CropDecl{{Left: -5, Right: 50, Top: 0, Bottom: 100, Source: probe.SourceClap}}
	if _, err := Resolve(tr); clock.AsKind(err) != clock.KindOutOfRange {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}

func TestConflictingRotationWarnsButKeepsFirst(t *testing.T) {
	tr := track(100, 100)
	tr.Rotations = []probe.RotationDecl{
		{Degrees: 90, Source: probe.SourceMXFRot},
		{Degrees: 180, Source: probe.SourceTkhd},
	}
	r, err := Resolve(tr)
	if err != nil {
		t.Fatal(err)
	}
	if r.Rotation != 90 {
		t.Fatalf("rotation = %d", r.Rotation)
	}
	if len(r.Warnings) == 0 {
		t.Fatal("conflicting rotation must warn")
	}
}

func TestNoColourIsUnresolvedNotInvented(t *testing.T) {
	r, err := Resolve(track(100, 100))
	if err != nil {
		t.Fatal(err)
	}
	if r.ColorResolved || r.Color != nil {
		t.Fatalf("colour invented: %+v", r.Color)
	}
	if r.ColorRule != RuleUnresolved {
		t.Fatalf("rule = %s", r.ColorRule)
	}
}

func TestApplyAllStopsOnIllegalTrack(t *testing.T) {
	bad := track(100, 100)
	bad.Crops = []probe.CropDecl{{Left: 0, Right: 200, Top: 0, Bottom: 100, Source: probe.SourceClap}}
	if _, err := ApplyAll([]*probe.Track{track(100, 100), bad}); err == nil {
		t.Fatal("ApplyAll must surface the illegal crop")
	}
}
