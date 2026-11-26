package parse

import (
	"testing"
)

func TestResolveMetadataSingleSource(t *testing.T) {
	srcs := []MetaSource{
		{Name: "mp4", Meta: &Meta{Rotation: &Rotation{Degrees: 90}}},
		{Name: "mxf", Meta: &Meta{}},
	}
	meta, choices, conflicts := ResolveMetadata(srcs, nil)
	if len(conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %#v", conflicts)
	}
	if meta.Rotation == nil || meta.Rotation.Degrees != 90 {
		t.Fatalf("rotation not adopted: %#v", meta.Rotation)
	}
	if len(choices) != 1 || !choices[0].Auto || choices[0].Source != "mp4" {
		t.Fatalf("choices: %#v", choices)
	}
}

func TestResolveMetadataAgreementIsAutomatic(t *testing.T) {
	c := &Color{Primaries: 1, Transfer: 1, Matrix: 1}
	srcs := []MetaSource{
		{Name: "mp4", Meta: &Meta{Color: c}},
		{Name: "mxf", Meta: &Meta{Color: &Color{Primaries: 1, Transfer: 1, Matrix: 1}}},
	}
	meta, choices, conflicts := ResolveMetadata(srcs, nil)
	if len(conflicts) != 0 || len(choices) != 1 || !choices[0].Auto {
		t.Fatalf("choices=%#v conflicts=%#v", choices, conflicts)
	}
	if meta.Color == nil || meta.Color.Matrix != 1 {
		t.Fatalf("color=%#v", meta.Color)
	}
}

func TestResolveMetadataConflictNeedsSelector(t *testing.T) {
	srcs := []MetaSource{
		{Name: "mp4", Meta: &Meta{Rotation: &Rotation{Degrees: 90}}},
		{Name: "mxf", Meta: &Meta{Rotation: &Rotation{Degrees: 180}}},
	}
	meta, choices, conflicts := ResolveMetadata(srcs, nil)
	if len(conflicts) != 1 || conflicts[0].Field != FieldRotation {
		t.Fatalf("conflicts=%#v", conflicts)
	}
	if meta.Rotation != nil {
		t.Fatalf("conflicted field must stay unset: %#v", meta.Rotation)
	}
	if !contains(conflicts[0].Reason, "not specified") {
		t.Fatalf("reason=%q", conflicts[0].Reason)
	}
	if len(conflicts[0].Candidates) != 2 {
		t.Fatalf("candidates=%#v", conflicts[0].Candidates)
	}

	meta, choices, conflicts = ResolveMetadata(srcs, map[string]string{FieldRotation: "mxf"})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts=%#v", conflicts)
	}
	if meta.Rotation == nil || meta.Rotation.Degrees != 180 {
		t.Fatalf("selected rotation=%#v", meta.Rotation)
	}
	if len(choices) != 1 || !choices[0].HadConflict || choices[0].Source != "mxf" {
		t.Fatalf("choices=%#v", choices)
	}
}

func TestResolveMetadataFieldSpecificSelection(t *testing.T) {
	srcs := []MetaSource{
		{Name: "mp4", Meta: &Meta{
			Rotation: &Rotation{Degrees: 90},
			Crop:     &Crop{Left: 2, Top: 2, Right: 2, Bottom: 2},
		}},
		{Name: "mxf", Meta: &Meta{
			Rotation: &Rotation{Degrees: 270},
			Color:    &Color{Primaries: 9, Transfer: 16, Matrix: 9},
		}},
	}
	meta, choices, conflicts := ResolveMetadata(srcs, map[string]string{
		FieldRotation: "mp4",
		FieldColor:    "mxf",
	})
	if len(conflicts) != 0 {
		t.Fatalf("conflicts=%#v", conflicts)
	}
	if meta.Rotation.Degrees != 90 || meta.Crop.Left != 2 || meta.Color.Primaries != 9 {
		t.Fatalf("meta=%#v", meta)
	}
	// The crop field only exists on one source, so it is automatic.
	byField := map[string]FieldChoice{}
	for _, c := range choices {
		byField[c.Field] = c
	}
	if !byField[FieldCrop].Auto || byField[FieldRotation].Auto || !byField[FieldRotation].HadConflict {
		t.Fatalf("choices=%#v", choices)
	}
}

func TestResolveMetadataSelectorWithoutValue(t *testing.T) {
	srcs := []MetaSource{
		{Name: "mp4", Meta: &Meta{Rotation: &Rotation{Degrees: 90}}},
		{Name: "mxf", Meta: &Meta{Rotation: &Rotation{Degrees: 180}}},
	}
	_, _, conflicts := ResolveMetadata(srcs, map[string]string{FieldRotation: "mxf"})
	if len(conflicts) != 0 {
		t.Fatalf("mxf has a value, should resolve: %#v", conflicts)
	}
	// Selecting a source that carries no value for the field is a conflict.
	_, _, conflicts = ResolveMetadata(srcs, map[string]string{FieldRotation: "other"})
	if len(conflicts) != 1 || !contains(conflicts[0].Reason, "no value") {
		t.Fatalf("conflicts=%#v", conflicts)
	}
}

func TestResolveMetadataDeterministicOrdering(t *testing.T) {
	c := &Color{Primaries: 1}
	srcs := []MetaSource{
		{Name: "zeta", Meta: &Meta{Rotation: &Rotation{Degrees: 1}, Color: c}},
		{Name: "alpha", Meta: &Meta{Rotation: &Rotation{Degrees: 2}, Color: c}},
	}
	_, choices1, conflicts1 := ResolveMetadata(srcs, nil)
	rev := []MetaSource{srcs[1], srcs[0]}
	_, choices2, conflicts2 := ResolveMetadata(rev, nil)
	if len(choices1) != len(choices2) || len(conflicts1) != len(conflicts2) {
		t.Fatal("resolution depends on input order")
	}
	if conflicts1[0].Candidates[0].Source != "alpha" {
		t.Fatalf("candidates not sorted: %#v", conflicts1[0].Candidates)
	}
	if len(choices1) != 2 || choices1[0].Field != FieldRotation || choices1[1].Field != FieldColor {
		t.Fatalf("choices not in descriptor order: %#v", choices1)
	}
}

func TestResolveMetadataAbsentEverywhere(t *testing.T) {
	meta, choices, conflicts := ResolveMetadata([]MetaSource{{Name: "a", Meta: &Meta{}}}, nil)
	if len(choices) != 0 || len(conflicts) != 0 {
		t.Fatalf("choices=%#v conflicts=%#v", choices, conflicts)
	}
	if meta.Rotation != nil || meta.Crop != nil || meta.Color != nil {
		t.Fatalf("meta=%#v", meta)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

func TestDescribeChoices(t *testing.T) {
	got := DescribeChoices([]FieldChoice{
		{Field: FieldRotation, Source: "mp4", Value: "90"},
		{Field: FieldCrop},
	})
	if got != "rotation=mp4(90);crop=unresolved" {
		t.Fatalf("got %q", got)
	}
}

func TestRotationMatrixMapping(t *testing.T) {
	cases := []int{0, 90, 180, 270}
	for _, deg := range cases {
		var m [9]int32
		switch deg {
		case 0:
			m = [9]int32{1 << 16, 0, 0, 0, 1 << 16, 0, 0, 0, 1 << 30}
		case 90:
			m = [9]int32{0, 1 << 16, 0, -1 << 16, 0, 0, 0, 0, 1 << 30}
		case 180:
			m = [9]int32{-1 << 16, 0, 0, 0, -1 << 16, 0, 0, 0, 1 << 30}
		case 270:
			m = [9]int32{0, -1 << 16, 0, 1 << 16, 0, 0, 0, 0, 1 << 30}
		}
		if got, ok := rotationFromMatrix(m); !ok || got != deg {
			t.Fatalf("deg %d -> %d %v", deg, got, ok)
		}
	}
	if _, ok := rotationFromMatrix([9]int32{}); ok {
		t.Fatal("all-zero matrix is not a rotation")
	}
}

func TestParseColrRejectsUnknownType(t *testing.T) {
	if _, ok := parseColr([]byte("prof1234")); ok {
		t.Fatal("prof should not parse as nclx")
	}
	col, ok := parseColr([]byte{'n', 'c', 'l', 'x', 0, 1, 0, 1, 0, 1, 0x80})
	if !ok || col.Primaries != 1 || !col.FullRange {
		t.Fatalf("col=%#v ok=%v", col, ok)
	}
}
