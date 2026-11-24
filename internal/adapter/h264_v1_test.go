package adapter

import (
	"testing"

	"medcheck/internal/clock"
)

func TestH264PreservesValidColorMetadata(t *testing.T) {
	h := NewH264V1()
	in := VideoInput{Width: 1280, Height: 720, Color: ColorMetadata{
		Present: true, Primaries: 1, Transfer: 1, Matrix: 1, Source: "bitstream:vui",
	}}
	out, errs := h.Adapt(in)
	if out.Spec.Color.Matrix != 1 || out.Spec.Color.Source != "bitstream:vui" {
		t.Fatalf("color=%+v", out.Spec.Color)
	}
	if errs[0].Code != clock.CodeColorMetadataPreserved {
		t.Fatalf("errs=%v", errs)
	}
}

func TestH264MissingColorUsesSourcedDefault(t *testing.T) {
	h := NewH264V1()
	out, errs := h.Adapt(VideoInput{Width: 640, Height: 360})
	if out.Spec.Color.Primaries != 1 || out.Spec.Color.Source == "" {
		t.Fatalf("default color missing source: %+v", out.Spec.Color)
	}
	if errs[0].Code != clock.CodeColorMetadataDefaulted || errs[0].Source == "" {
		t.Fatalf("errs=%v", errs)
	}
}

func TestH264ConflictUsesExplicitConflictPolicy(t *testing.T) {
	h := NewH264V1()
	in := VideoInput{Color: ColorMetadata{
		Present: true, Primaries: 1, Transfer: 6, Matrix: 6, // 709 primaries, 601 transfer/matrix
	}}
	out, errs := h.Adapt(in)
	if errs[0].Code != clock.CodeColorMetadataConflict {
		t.Fatalf("expected conflict, got %v", errs)
	}
	if out.Spec.Color.Source != "policy:conflict-default-bt709-v1" || out.Spec.Color.Matrix != 1 {
		t.Fatalf("conflict fallback not explicit: %+v", out.Spec.Color)
	}
}
