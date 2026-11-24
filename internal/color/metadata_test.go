package color

import (
	"testing"

	"mediapipe/internal/model"
)

func framesWith(labels ...string) []model.Frame {
	out := make([]model.Frame, len(labels))
	for i, l := range labels {
		out[i] = model.Frame{
			TrackID: "v", Index: i,
			Color: model.ColorMeta{Label: l, Source: "manifest"},
		}
	}
	return out
}

func TestNormalizeKnownLabels(t *testing.T) {
	res, err := Normalize("v", framesWith("bt601", "bt709"), "bt709")
	if err != nil || res.Blocked {
		t.Fatalf("normalize failed: %v", err)
	}
	if len(res.Frames) != 2 {
		t.Fatalf("frame results = %d", len(res.Frames))
	}
	if !res.Frames[0].Changed || res.Frames[1].Changed {
		t.Fatal("change flag wrong")
	}
	after := res.Frames[0].After
	if after.Label != "bt709" || after.Matrix != "bt709" {
		t.Fatalf("normalized meta = %+v", after)
	}
	// Provenance of the original tag must survive normalization.
	if after.Source != "normalized:bt601|manifest" {
		t.Fatalf("provenance lost: %q", after.Source)
	}
}

func TestUnknownLabelBlocks(t *testing.T) {
	res, err := Normalize("v", framesWith("bt709", "acme-hdr"), "bt709")
	if err == nil {
		t.Fatal("unknown label must error")
	}
	me := model.AsMediaError(err)
	if me == nil || me.Type != model.ErrUnknownColor || me.FrameIndex != 1 || me.TrackID != "v" {
		t.Fatalf("error = %v", err)
	}
	if !res.Blocked || res.FirstUnknown == nil || len(res.Frames) != 1 {
		t.Fatal("track must be marked blocked at the first unknown frame")
	}
	// Explicit sentinel and empty labels are also refused — never guessed.
	for _, l := range []string{"unknown", ""} {
		if _, err := Normalize("v", framesWith(l), "bt709"); model.AsMediaError(err) == nil {
			t.Fatalf("label %q must not be guessed", l)
		}
	}
}

func TestInvalidTarget(t *testing.T) {
	me := model.AsMediaError(func() error {
		_, e := Normalize("v", framesWith("bt709"), "made-up")
		return e
	}())
	if me == nil || me.Type != model.ErrInvalidConfig {
		t.Fatalf("target validation: %v", me)
	}
}

func TestCatalogIsClosedAndStable(t *testing.T) {
	got := KnownLabels()
	want := []string{"bt2020", "bt601", "bt709", "srgb"}
	if len(got) != len(want) {
		t.Fatalf("labels = %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("labels = %v, want %v", got, want)
		}
	}
}
