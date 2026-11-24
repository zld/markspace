package color_test

import (
	"bytes"
	"testing"

	"mediacheck/color"
	"mediacheck/ebml"
	"mediacheck/internal/fixture"
	"mediacheck/timeline"
)

func scanColors(t *testing.T, cfg fixture.Config) (track, container *ebml.ColorMeta) {
	t.Helper()
	stream, err := fixture.Build(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, err := ebml.Scan(bytes.NewReader(stream))
	if err != nil {
		t.Fatal(err)
	}
	for _, tr := range res.Tracks {
		if tr.IsVideo() {
			track = tr.Color
		}
	}
	return track, res.ContainerColor
}

func TestResolveTrackWinsByPolicy(t *testing.T) {
	track, container := scanColors(t, fixture.Config{
		Seed: 1, Frames: 1, ColorConflict: true,
	})
	rec, err := color.Resolve(track, container, color.Policy{PreferTrack: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Source != color.SourceTrack || rec.Matrix != 9 || !rec.Conflict {
		t.Fatalf("track-priority resolution wrong: %+v", rec)
	}
	if ok, _ := rec.SafeToConvert(); !ok {
		t.Fatal("BT.709 record should be safe to convert")
	}
}

func TestResolveContainerWinsByPolicy(t *testing.T) {
	track, container := scanColors(t, fixture.Config{
		Seed: 1, Frames: 1, ColorConflict: true,
	})
	rec, err := color.Resolve(track, container, color.Policy{PreferTrack: false})
	if err != nil {
		t.Fatal(err)
	}
	if rec.Source != color.SourceContainer || rec.Matrix != 6 || !rec.Conflict {
		t.Fatalf("container-priority resolution wrong: %+v", rec)
	}
}

func TestResolveMissingSide(t *testing.T) {
	track, _ := scanColors(t, fixture.Config{Seed: 1, Frames: 1})
	rec, err := color.Resolve(track, nil, color.Policy{PreferTrack: true})
	if err != nil || rec.Conflict || rec.Source != color.SourceTrack {
		t.Fatalf("single-side resolution wrong: %+v %v", rec, err)
	}
	d, err := color.Resolve(nil, nil, color.Policy{})
	if err != nil || d.Source != color.SourceDefault {
		t.Fatalf("default resolution wrong: %+v", d)
	}
}

func TestUnknownEnumBlocksConversion(t *testing.T) {
	track, container := scanColors(t, fixture.Config{
		Seed: 1, Frames: 1, ColorConflict: true, UnknownColor: true,
	})
	rec, err := color.Resolve(track, container, color.Policy{PreferTrack: true})
	if err != nil {
		t.Fatal(err)
	}
	if rec.RawEnum != 0x2A2A {
		t.Fatalf("raw enum must be retained, got %d", rec.RawEnum)
	}
	ok, why := rec.SafeToConvert()
	if ok || why == "" {
		t.Fatal("unknown enumeration must block conversion")
	}
	if err := rec.RequireSafe(); !timeline.IsCategory(err, timeline.CatUnsupported) {
		t.Fatalf("RequireSafe must return unsupported, got %v", err)
	}
}

func TestUnknownStandardValueBlocks(t *testing.T) {
	meta := &ebml.ColorMeta{Matrix: 9, Transfer: 16, Primaries: 9, RangeFlag: 1}
	rec, err := color.Resolve(meta, nil, color.Policy{PreferTrack: true})
	if err != nil {
		t.Fatal(err)
	}
	rec.Matrix = 42 // unknown matrix coefficient
	ok, _ := rec.SafeToConvert()
	if ok {
		t.Fatal("unknown matrix must block conversion")
	}
}
