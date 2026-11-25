package meta

import (
	"testing"

	"rtpreplay/media"
)

func TestNormalizePriorityWins(t *testing.T) {
	cands := []Candidate{
		{Field: FieldRotation, Source: "mxf:cdci", Priority: PriorityMXF,
			Rotation: &Rotation{Degrees: 180}},
		{Field: FieldRotation, Source: "mp4:tkhd", Priority: PriorityMP4,
			Rotation: &Rotation{Degrees: 90}},
	}
	n, err := Normalize(cands)
	if err != nil {
		t.Fatal(err)
	}
	if n.Rotation.Degrees != 90 {
		t.Fatalf("MP4 priority must win, got %d", n.Rotation.Degrees)
	}
	var chosen, traces int
	for _, tr := range n.Traces {
		if tr.Field == FieldRotation {
			traces++
			if tr.Chosen {
				chosen++
				if tr.Source != "mp4:tkhd" {
					t.Fatalf("wrong chosen source %s", tr.Source)
				}
			}
		}
	}
	if traces != 2 || chosen != 1 {
		t.Fatalf("trace accounting wrong: traces=%d chosen=%d", traces, chosen)
	}
}

func TestNormalizeLowerPriorityFillsGap(t *testing.T) {
	cands := []Candidate{
		{Field: FieldColor, Source: "mxf:cdci", Priority: PriorityMXF,
			Color: &Color{ColourType: "nclx", ColourPrimaries: 9, TransferCharacteristics: 16, MatrixCoefficients: 9}},
	}
	n, err := Normalize(cands)
	if err != nil {
		t.Fatal(err)
	}
	if n.Color == nil || n.Color.ColourPrimaries != 9 {
		t.Fatal("MXF color should survive when MP4 provides none")
	}
}

func TestNormalizeEqualPriorityConflict(t *testing.T) {
	cands := []Candidate{
		{Field: FieldColor, Source: "mp4:colr:nclx@trak0", Priority: PriorityMP4,
			Color: &Color{ColourType: "nclx", ColourPrimaries: 9}},
		{Field: FieldColor, Source: "mp4:colr:nclx@trak1", Priority: PriorityMP4,
			Color: &Color{ColourType: "nclx", ColourPrimaries: 1}},
	}
	_, err := Normalize(cands)
	le, ok := media.AsLocatable(err)
	if !ok || le.Kind != media.KindConflict || le.Code != "METADATA_CONFLICT" {
		t.Fatalf("want METADATA_CONFLICT, got %v", err)
	}
}

func TestNormalizeEqualPriorityAgrees(t *testing.T) {
	rot := &Rotation{Degrees: 270}
	cands := []Candidate{
		{Field: FieldRotation, Source: "mp4:tkhd@0", Priority: PriorityMP4, Rotation: rot},
		{Field: FieldRotation, Source: "mp4:tkhd@1", Priority: PriorityMP4, Rotation: rot},
	}
	n, err := Normalize(cands)
	if err != nil {
		t.Fatalf("identical equal-priority values must not conflict: %v", err)
	}
	if n.Rotation.Degrees != 270 {
		t.Fatalf("got %d", n.Rotation.Degrees)
	}
}

func TestNormalizeCropAndMissing(t *testing.T) {
	cands := []Candidate{
		{Field: FieldCrop, Source: "mp4:clap", Priority: PriorityMP4,
			Crop: &Crop{Left: 10, Top: 20, Width: 1920, Height: 1080}},
	}
	n, err := Normalize(cands)
	if err != nil {
		t.Fatal(err)
	}
	if n.Crop == nil || n.Crop.Left != 10 || n.Color != nil {
		t.Fatalf("unexpected result: %+v", n)
	}
}

func TestValidateRotation(t *testing.T) {
	if err := ValidateRotation(Rotation{Degrees: 90}); err != nil {
		t.Fatal(err)
	}
	if le, ok := media.AsLocatable(ValidateRotation(Rotation{Degrees: 45})); !ok || le.Code != "BAD_ROTATION" {
		t.Fatalf("want BAD_ROTATION, got %v", le)
	}
}

func TestValidateColor(t *testing.T) {
	good := &Color{ColourType: "nclx", ColourPrimaries: 9, TransferCharacteristics: 16, MatrixCoefficients: 9}
	if err := ValidateColor(good); err != nil {
		t.Fatal(err)
	}
	bad := &Color{ColourType: "nclx", ColourPrimaries: 0}
	if le, ok := media.AsLocatable(ValidateColor(bad)); !ok || le.Code != "BAD_COLOR_VALUE" {
		t.Fatalf("want BAD_COLOR_VALUE, got %v", bad)
	}
	unknown := &Color{ColourType: "xxxx"}
	if le, ok := media.AsLocatable(ValidateColor(unknown)); !ok || le.Code != "BAD_COLOR_TYPE" {
		t.Fatalf("want BAD_COLOR_TYPE, got %v", unknown)
	}
}

func TestRotationFromMatrix(t *testing.T) {
	const one = 0x00010000
	cases := []struct {
		m [9]int32
		d int
	}{
		{[9]int32{one, 0, 0, 0, one, 0, 0, 0, 0x40000000}, 0},
		{[9]int32{0, one, 0, -one, 0, 0, 0, 0, 0x40000000}, 90},
		{[9]int32{-one, 0, 0, 0, -one, 0, 0, 0, 0x40000000}, 180},
		{[9]int32{0, -one, 0, one, 0, 0, 0, 0, 0x40000000}, 270},
	}
	for i, tc := range cases {
		if got := RotationFromMatrix(tc.m); got.Degrees != tc.d {
			t.Fatalf("case %d: got %d want %d", i, got.Degrees, tc.d)
		}
	}
}
