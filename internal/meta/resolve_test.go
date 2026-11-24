package meta

import (
	"testing"

	"mediad/internal/clock"
)

func mxfFixtureTrack() *TrackMeta {
	return &TrackMeta{
		TrackNo: 1, Kind: "video", Clock: clock.Rate{90000, 1}, Width: 1920, Height: 1080,
		Rotation: 90, RotSrc: SourceMXF,
		Crop:  Crop{X: 0, Y: 0, Width: 1280, Height: 720, Source: SourceMXF},
		Color: Color{"bt2020", "smpte2084", "bt2020nc", SourceMXF},
	}
}

func mp4FixtureTrack() *TrackMeta {
	return &TrackMeta{
		TrackNo: 1, Kind: "video", Clock: clock.Rate{90000, 1}, Width: 1920, Height: 1080,
		Rotation: 270, RotSrc: SourceMP4,
		Crop:  Crop{X: 8, Y: 8, Width: 100, Height: 100, Source: SourceMP4},
		Color: Color{"bt709", "bt709", "bt709", SourceMP4},
	}
}

func TestResolveMXFWinsConflicts(t *testing.T) {
	out := Resolve(mxfFixtureTrack(), mp4FixtureTrack())
	if out.Rotation != 90 || out.RotSrc != SourceMXF {
		t.Fatalf("rotation winner = %d/%s", out.Rotation, out.RotSrc)
	}
	if out.Crop.Width != 1280 || out.Crop.Source != SourceMXF {
		t.Fatalf("crop winner = %+v", out.Crop)
	}
	if out.Color.Primaries != "bt2020" || out.Color.Source != SourceMXF {
		t.Fatalf("color winner = %+v", out.Color)
	}
	if len(out.Conflicts) != 3 {
		t.Fatalf("want 3 conflicts (rotation/crop/color), got %d: %+v", len(out.Conflicts), out.Conflicts)
	}
	seen := map[string]bool{}
	for _, c := range out.Conflicts {
		seen[c.Field] = true
		if c.Chosen != SourceMXF || c.Values[SourceMXF] == "" || c.Values[SourceMP4] == "" {
			t.Fatalf("conflict missing provenance: %+v", c)
		}
	}
	for _, f := range []string{"rotation", "crop", "color"} {
		if !seen[f] {
			t.Fatalf("missing conflict for field %s", f)
		}
	}
}

func TestResolveNoConflictWhenAgreeing(t *testing.T) {
	m := mxfFixtureTrack()
	p := mp4FixtureTrack()
	p.Rotation, p.Crop, p.Color = m.Rotation, m.Crop, m.Color
	out := Resolve(m, p)
	if len(out.Conflicts) != 0 {
		t.Fatalf("agreement must not produce conflicts: %+v", out.Conflicts)
	}
}

func TestResolveMP4OnlyAndDefaults(t *testing.T) {
	// MP4-only track: MP4 values survive, missing MXF does not demote source.
	out := Resolve(nil, mp4FixtureTrack())
	if out.Rotation != 270 || out.RotSrc != SourceMP4 || out.Color.Source != SourceMP4 {
		t.Fatalf("mp4-only track wrong: %+v", out)
	}

	// Neither container authors crop/color: explicit defaults, no conflict.
	bare := &TrackMeta{TrackNo: 7, Kind: "video", Clock: clock.Rate{90000, 1},
		Width: 640, Height: 360}
	out = Resolve(bare, &TrackMeta{TrackNo: 7, Clock: clock.Rate{90000, 1}, Width: 640, Height: 360})
	if out.Crop.Source != SourceDefault || out.Crop.Width != 640 || out.Crop.Height != 360 {
		t.Fatalf("default crop wrong: %+v", out.Crop)
	}
	if out.Color.Source != SourceDefault || out.Color.Primaries != DefaultColorPrimaries {
		t.Fatalf("default color wrong: %+v", out.Color)
	}
	if out.RotSrc != SourceDefault || out.Rotation != 0 {
		t.Fatalf("default rotation wrong: %d/%s", out.Rotation, out.RotSrc)
	}
	if len(out.Conflicts) != 0 {
		t.Fatalf("defaults must not produce conflicts: %+v", out.Conflicts)
	}
}

func TestResolveAllPairingAndErrors(t *testing.T) {
	mxfRes := &Result{Tracks: []*TrackMeta{mxfFixtureTrack()}}
	mp4Res := &Result{
		Tracks: []*TrackMeta{mp4FixtureTrack(), {TrackNo: 9, Kind: "audio", Clock: clock.Rate{48000, 1}}},
	}
	out := ResolveAll(mxfRes, mp4Res)
	if len(out.Tracks) != 2 {
		t.Fatalf("want 2 merged tracks, got %d", len(out.Tracks))
	}
	if out.Tracks[0].TrackNo != 1 || out.Tracks[1].TrackNo != 9 {
		t.Fatalf("track order/identity wrong: %+v", out.Tracks)
	}
	if out.ConflictCount() != 3 {
		t.Fatalf("ConflictCount = %d, want 3", out.ConflictCount())
	}
	// Inputs must stay immutable.
	if len(mxfRes.Tracks[0].Conflicts) != 0 {
		t.Fatal("Resolve mutated its MXF input")
	}
}
