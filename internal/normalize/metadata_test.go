package normalize

import (
	"strings"
	"testing"

	"archive/internal/clock"
	"archive/internal/probe"
)

func track(width, height int) *probe.Track {
	return &probe.Track{ID: "v1", Format: "mp4", Kind: "video", StoredWidth: width, StoredHeight: height}
}

func TestExplicitCropBeatsDefaultFullFrame(t *testing.T) {
	tr := track(1920, 1080)
	tr.Crops = []probe.CropDecl{
		{Left: 0, Right: 1920, Top: 0, Bottom: 1080, Source: "default.full"},
		{Left: 240, Right: 1680, Top: 0, Bottom: 1080, Source: probe.SourceClap},
	}
	r, err := Resolve(tr)
	if err != nil {
		t.Fatal(err)
	}
	if r.CropRule != RuleExplicitCrop || r.CropSource != probe.SourceClap {
		t.Fatalf("rule=%s source=%s", r.CropRule, r.CropSource)
	}
	if r.Crop.Width() != 1440 || r.OutputWidth() != 1440 {
		t.Fatalf("crop width=%d output=%d", r.Crop.Width(), r.OutputWidth())
	}
	if len(r.Warnings) == 0 {
		t.Fatal("full-frame crop should warn about supersession")
	}
}

func TestDefaultCropWhenNoneDeclared(t *testing.T) {
	r, err := Resolve(track(1280, 720))
	if err != nil {
		t.Fatal(err)
	}
	if r.CropRule != RuleDefaultFull || r.Crop.Right != 1280 {
		t.Fatalf("rule=%s crop=%+v", r.CropRule, r.Crop)
	}
}

func TestIllegalCropIsTypedError(t *testing.T) {
	tr := track(1920, 1080)
	tr.Crops = []probe.CropDecl{{Left: 0, Right: 2000, Top: 0, Bottom: 1080, Source: probe.SourceClap}}
	_, err := Resolve(tr)
	if clock.AsKind(err) != clock.KindOutOfRange {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
	if !strings.Contains(err.Error(), "track v1") {
		t.Fatalf("error lacks track id: %v", err)
	}
}

func TestRotationExplicitPerTrack(t *testing.T) {
	tr := track(1920, 1080)
	tr.Rotations = []probe.RotationDecl{{Degrees: 90, Source: probe.SourceMXFRot}}
	r, err := Resolve(tr)
	if err != nil {
		t.Fatal(err)
	}
	if r.Rotation != 90 || r.RotationRule != RuleTrackRotation {
		t.Fatalf("rotation=%d rule=%s", r.Rotation, r.RotationRule)
	}
	if r.OutputWidth() != 1080 || r.OutputHeight() != 1920 {
		t.Fatalf("rotated output = %dx%d", r.OutputWidth(), r.OutputHeight())
	}
}

func TestColourConflictUnresolvedNotSilentlyOverwritten(t *testing.T) {
	tr := track(1920, 1080)
	tr.Colors = []probe.ColorDecl{
		{Primaries: 1, Transfer: 1, Matrix: 1, Source: probe.SourceColr},
		{Primaries: 9, Transfer: 16, Matrix: 9, FullRange: true, Source: probe.SourceMXFCol},
	}
	r, err := Resolve(tr)
	if err != nil {
		t.Fatal(err)
	}
	if r.ColorResolved || !r.ColorConflict || r.ColorRule != RuleUnresolved {
		t.Fatalf("resolved=%t conflict=%t rule=%s", r.ColorResolved, r.ColorConflict, r.ColorRule)
	}
	if len(r.ColorSources) != 2 {
		t.Fatalf("sources = %v", r.ColorSources)
	}
	if r.Color == nil || r.Color.Primaries != 1 {
		t.Fatalf("primary provenance lost: %+v", r.Color)
	}
}

func TestColourSingleDeclarationResolves(t *testing.T) {
	tr := track(1920, 1080)
	tr.Colors = []probe.ColorDecl{{Primaries: 9, Transfer: 16, Matrix: 9, Source: probe.SourceColr}}
	r, _ := Resolve(tr)
	if !r.ColorResolved || r.ColorRule != RuleColorSingle {
		t.Fatalf("resolved=%t rule=%s", r.ColorResolved, r.ColorRule)
	}
}

func TestInvalidStoredDimensionsRejected(t *testing.T) {
	_, err := Resolve(&probe.Track{ID: "v9"})
	if clock.AsKind(err) != clock.KindInvalidMetadata {
		t.Fatalf("kind = %q", clock.AsKind(err))
	}
}
