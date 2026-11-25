package mp4

import (
	"testing"

	auditerrors "media-audit/internal/errors"
	"media-audit/internal/media"
	"media-audit/internal/timebase"
)

func TestBuildAndParseTrackMetadata(t *testing.T) {
	req := TrackRequest{
		ID: 42, Timebase: timebase.MP4(48000), Width: 320, Height: 240,
		Metadata: media.Metadata{
			HasRotation: true, RotationDegrees: 90,
			Crop:  &media.Crop{Top: 8, Right: 8, Bottom: 8, Left: 8},
			Color: &media.Color{Primaries: 9, Transfer: 9, Matrix: 9, FullRange: true},
		},
		Samples: []media.SampleSpec{{KeyFrame: true, PTS: 0, Duration: 48000, Sequence: 1, Timestamp: 0, Payload: []byte("x")}},
	}
	doc := Parse("memory.mp4", BuildTrackFile(req))
	if len(doc.Errors) != 0 {
		t.Fatalf("parse errors: %+v", doc.Errors)
	}
	if len(doc.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(doc.Tracks))
	}
	tr := doc.Tracks[0]
	if tr.ID != 42 || tr.Timebase.Numerator != 48000 {
		t.Fatalf("unexpected track id/timebase: %+v", tr)
	}
	if !tr.Metadata.HasRotation || tr.Metadata.RotationDegrees != 90 {
		t.Fatalf("rotation not parsed: %+v", tr.Metadata)
	}
	if tr.Metadata.Crop == nil || *tr.Metadata.Crop != (media.Crop{Top: 8, Right: 8, Bottom: 8, Left: 8}) {
		t.Fatalf("crop not parsed: %+v", tr.Metadata.Crop)
	}
	if tr.Metadata.Color == nil || tr.Metadata.Color.Primaries != 9 || !tr.Metadata.Color.FullRange {
		t.Fatalf("color not parsed: %+v", tr.Metadata.Color)
	}
	if tr.TimeRange == nil || tr.TimeRange.EndTicks != 90000 {
		t.Fatalf("unified range = %+v", tr.TimeRange)
	}
}

func TestInvalidBoxSizeReportsHeaderOffset(t *testing.T) {
	req := TrackRequest{ID: 1, Timebase: timebase.MP4(90000)}
	data := BuildTrackFile(req)
	doc := Parse("x.mp4", data)
	var off int64
	for _, el := range doc.Elements {
		if el.Type == "asmp" {
			off = el.Offset
		}
	}
	if off == 0 {
		t.Fatal("asmp box not found")
	}
	for i := 0; i < 4; i++ {
		data[off+int64(i)] = 0
	}
	data[off+3] = 4
	doc = Parse("x.mp4", data)
	var found bool
	for _, e := range doc.Errors {
		if e.Code == auditerrors.CodeInvalidLength && e.Offset == off {
			found = true
		}
	}
	if !found {
		t.Fatalf("invalid box length at %d not reported: %+v", off, doc.Errors)
	}
}
