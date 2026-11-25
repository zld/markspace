package verify

import (
	"bytes"
	"fmt"
	"reflect"

	"medpipe/internal/ingest"
)

// RoundTripReport quantifies field-level encode→parse consistency.
type RoundTripReport struct {
	FrameCount    int      `json:"frame_count"`
	TotalFields   int      `json:"total_fields"`
	MatchedFields int      `json:"matched_fields"`
	Mismatches    []string `json:"mismatches,omitempty"`
}

// Rate returns matched/total as a percentage.
func (r RoundTripReport) Rate() float64 {
	if r.TotalFields == 0 {
		return 100
	}
	return 100 * float64(r.MatchedFields) / float64(r.TotalFields)
}

// CheckRoundTrip encodes every frame, parses the concatenated legal stream,
// and compares all persistent fields except the source-only input offset.
func CheckRoundTrip(frames []*ingest.Frame) (RoundTripReport, error) {
	var stream bytes.Buffer
	for _, f := range frames {
		b, err := ingest.EncodeFrame(f)
		if err != nil {
			return RoundTripReport{}, err
		}
		stream.Write(b)
	}
	parsed, err := ingest.ParseStream(&stream, ingest.Options{})
	if err != nil {
		return RoundTripReport{}, err
	}
	return CompareFrames(frames, parsed.Frames), nil
}

// CompareFrames performs the field-by-field consistency calculation.
func CompareFrames(want []*ingest.Frame, got []*ingest.Frame) RoundTripReport {
	r := RoundTripReport{FrameCount: len(want)}
	if len(want) != len(got) {
		r.Mismatches = append(r.Mismatches, fmt.Sprintf("frame count want=%d got=%d", len(want), len(got)))
	}
	n := min(len(want), len(got))
	for i := 0; i < n; i++ {
		w, g := want[i], got[i]
		check := func(field string, a, b any) {
			r.TotalFields++
			if reflect.DeepEqual(a, b) {
				r.MatchedFields++
			} else {
				r.Mismatches = append(r.Mismatches, fmt.Sprintf("frame %d %s: want=%v got=%v", i, field, a, b))
			}
		}
		check("track_id", w.TrackID, g.TrackID)
		check("pts", w.PTS, g.PTS)
		check("timebase_num", w.Timebase.Numerator, g.Timebase.Numerator)
		check("timebase_den", w.Timebase.Denominator, g.Timebase.Denominator)
		check("width", w.Width, g.Width)
		check("height", w.Height, g.Height)
		check("key_frame", w.KeyFrame, g.KeyFrame)
		check("pixel_format", w.PixelFormat, g.PixelFormat)
		check("color_primaries", w.Color.Primaries, g.Color.Primaries)
		check("color_transfer", w.Color.Transfer, g.Color.Transfer)
		check("color_matrix", w.Color.Matrix, g.Color.Matrix)
		check("color_full_range", w.Color.FullRange, g.Color.FullRange)
		check("pixels", bytes.Equal(w.Pixels, g.Pixels), true)
		check("sha256", w.SHA256, g.SHA256)
	}
	return r
}
