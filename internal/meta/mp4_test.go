package meta

import (
	"testing"

	"mediad/internal/model"
)

func TestParseMP4HappyPath(t *testing.T) {
	in := []MP4Input{
		{
			TrackNo: 1, Kind: "video", ClockNum: 90000, Width: 1920, Height: 1080,
			Rotation: 90,
			Crop:  Crop{X: 4, Y: 8, Width: 1000, Height: 500, Source: SourceMP4},
			Color: Color{"bt2020", "smpte2084", "bt2020nc", SourceMP4},
		},
		{TrackNo: 2, Kind: "audio", ClockNum: 48000},
	}
	res := ParseMP4(BuildMP4(in))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %+v", res.Errors)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(res.Tracks))
	}
	tr := res.Tracks[0]
	if tr.Rotation != 90 || tr.RotSrc != SourceMP4 {
		t.Fatalf("rotation = %d (%s), want 90 from mp4", tr.Rotation, tr.RotSrc)
	}
	if tr.Width != 1920 || tr.Height != 1080 {
		t.Fatalf("dimensions = %dx%d", tr.Width, tr.Height)
	}
	if tr.Crop.Width != 1000 || tr.Crop.X != 4 || tr.Crop.Source != SourceMP4 {
		t.Fatalf("crop wrong: %+v", tr.Crop)
	}
	if tr.Color.Primaries != "bt2020" || tr.Color.Transfer != "smpte2084" {
		t.Fatalf("color wrong: %+v", tr.Color)
	}
	if tr.Clock.Num != 90000 || res.Tracks[1].Kind != "audio" {
		t.Fatalf("clock/kind wrong: %+v %+v", tr.Clock, res.Tracks[1])
	}
}

func TestParseMP4Rotations(t *testing.T) {
	for _, deg := range []int{0, 90, 180, 270} {
		in := validMP4Track(1)
		in.Rotation = deg
		res := ParseMP4(BuildMP4([]MP4Input{in}))
		if len(res.Errors) != 0 || len(res.Tracks) != 1 || res.Tracks[0].Rotation != deg {
			t.Fatalf("rotation %d: tracks=%+v errs=%+v", deg, res.Tracks, res.Errors)
		}
	}
}

func TestParseMP4CorruptInputs(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		code model.ErrorCode
	}{
		{"truncated header", CorruptMP4TruncatedHeader(), model.ErrMetaUnparseable},
		{"oversize box", CorruptMP4OversizeBox(), model.ErrMetaUnparseable},
		{"bad matrix", CorruptMP4BadMatrix(), model.ErrMetaUnparseable},
		{"invalid crop", CorruptMP4InvalidCrop(), model.ErrInvalidCrop},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := ParseMP4(c.data)
			if len(res.Errors) == 0 {
				t.Fatalf("%s: expected error", c.name)
			}
			e := res.Errors[0]
			if e.Code != c.code {
				t.Fatalf("%s: code=%s want %s (%s)", c.name, e.Code, c.code, e.Message)
			}
			if e.Offset < 0 && c.name != "bad matrix" && c.name != "invalid crop" {
				t.Fatalf("%s: error missing byte offset: %+v", c.name, e)
			}
		})
	}
}

func TestParseMP4DefaultsAndIndependence(t *testing.T) {
	// Track without clap/colr must take explicit defaults.
	plain := validMP4Track(1)
	plain.OmitCrop, plain.OmitColor = true, true
	data := BuildMP4([]MP4Input{plain, {TrackNo: 3, Kind: "audio", ClockNum: 48000}})
	data = append(data, CorruptMP4InvalidCrop()...)
	res := ParseMP4(data)
	if len(res.Tracks) != 2 {
		t.Fatalf("good tracks must survive, got %d (errors=%+v)", len(res.Tracks), res.Errors)
	}
	if got := res.Tracks[0]; got.Crop.Source != SourceDefault || got.Color.Source != SourceDefault {
		t.Fatalf("expected default crop/color, got %+v %+v", got.Crop, got.Color)
	}
	if len(res.Errors) != 1 || res.Errors[0].Track != 2 {
		t.Fatalf("expected one track-2 error, got %+v", res.Errors)
	}
}
