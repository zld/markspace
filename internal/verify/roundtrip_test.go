package verify

import (
	"testing"

	"medpipe/internal/clock"
	"medpipe/internal/ingest"
)

func TestCheckRoundTripExactFields(t *testing.T) {
	frames := []*ingest.Frame{
		{
			TrackID: 1, PTS: 3000, Timebase: clock.RTP90K, Width: 4, Height: 2,
			KeyFrame: true, PixelFormat: ingest.PixelRGB24,
			Color:  ingest.ColorMetadata{Primaries: 9, Transfer: 13, Matrix: 1, FullRange: true},
			Pixels: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24},
		},
	}
	rep, err := CheckRoundTrip(frames)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Rate() != 100 || len(rep.Mismatches) != 0 {
		t.Fatalf("report=%+v", rep)
	}
	if rep.MatchedFields < 14 || rep.MatchedFields != rep.TotalFields {
		t.Fatalf("field counts %d/%d", rep.MatchedFields, rep.TotalFields)
	}
}

func TestCompareFramesDetectsMismatch(t *testing.T) {
	w := &ingest.Frame{TrackID: 1, Width: 2, Height: 1}
	g := &ingest.Frame{TrackID: 2, Width: 2, Height: 1}
	rep := CompareFrames([]*ingest.Frame{w}, []*ingest.Frame{g})
	if rep.Rate() >= 100 || len(rep.Mismatches) == 0 {
		t.Fatalf("expected mismatch report: %+v", rep)
	}
}
