package meta

import (
	"testing"

	"mediad/internal/model"
)

func TestParseMXFHappyPath(t *testing.T) {
	in := []MXFInput{
		{
			TrackNo: 1, Kind: "video", ClockNum: 90000, ClockDen: 1,
			Width: 1920, Height: 1080, Rotation: 90,
			Crop:  Crop{X: 10, Y: 20, Width: 100, Height: 200, Source: SourceMXF},
			Color: Color{"bt2020", "smpte2084", "bt2020nc", SourceMXF},
		},
		{
			TrackNo: 2, Kind: "audio", ClockNum: 48000, ClockDen: 1,
			Crop: FullFrame(0, 0), Color: Color{Source: SourceMXF},
		},
	}
	res := ParseMXF(BuildMXF(in))
	if len(res.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", res.Errors)
	}
	if len(res.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(res.Tracks))
	}
	tr := res.Tracks[0]
	if tr.TrackNo != 1 || tr.Rotation != 90 || tr.RotSrc != SourceMXF {
		t.Fatalf("track fields wrong: %+v", tr)
	}
	if tr.Crop.X != 10 || tr.Crop.Width != 100 || tr.Color.Primaries != "bt2020" {
		t.Fatalf("crop/color wrong: %+v %+v", tr.Crop, tr.Color)
	}
	if tr.Clock.Num != 90000 || res.Tracks[1].Kind != "audio" {
		t.Fatalf("clock/kind wrong: %+v", tr.Clock)
	}
}

func TestParseMXFCorruptInputs(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		code model.ErrorCode
	}{
		{"truncated key", CorruptMXFTruncatedKey(), model.ErrMetaUnparseable},
		{"bad BER", CorruptMXFBadBER(), model.ErrMetaUnparseable},
		{"body overrun", CorruptMXFBodyOverrun(), model.ErrMetaUnparseable},
		{"invalid crop", CorruptMXFInvalidCrop(), model.ErrInvalidCrop},
		{"short local item", CorruptMXFShortItem(), model.ErrMetaUnparseable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := ParseMXF(c.data)
			if len(res.Errors) == 0 {
				t.Fatalf("%s: expected error, got tracks=%d", c.name, len(res.Tracks))
			}
			e := res.Errors[0]
			if e.Code != c.code {
				t.Fatalf("%s: code=%s want %s", c.name, e.Code, c.code)
			}
			if e.Offset < 0 {
				t.Fatalf("%s: error missing byte offset: %+v", c.name, e)
			}
		})
	}
}

func TestParseMXFIndependentTracksSurvive(t *testing.T) {
	// A good track, then a corrupt crop track, then another good track.
	data := BuildMXF([]MXFInput{validMXFTrack(1), validMXFTrack(3)})
	data = append(data, CorruptMXFInvalidCrop()...)
	res := ParseMXF(data)
	if len(res.Tracks) != 2 {
		t.Fatalf("good tracks must survive, got %d", len(res.Tracks))
	}
	if len(res.Errors) != 1 || res.Errors[0].Track != 2 {
		t.Fatalf("expected one track-2 error, got %+v", res.Errors)
	}
	if codes := res.ErrorCodes(); len(codes) != 1 || codes[0] != string(model.ErrInvalidCrop) {
		t.Fatalf("ErrorCodes() = %v", codes)
	}
}

func TestBERRoundTrip(t *testing.T) {
	for _, v := range []int64{0, 1, 127, 128, 300, 65535, 1 << 20} {
		enc := encodeBER(v)
		dec, n, err := decodeBER(enc)
		if err != nil || dec != v || n != len(enc) {
			t.Fatalf("BER round trip %d -> %v -> %d (n=%d) err=%v", v, enc, dec, n, err)
		}
	}
}
