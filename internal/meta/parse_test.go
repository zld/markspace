package meta

import (
	"bytes"
	"errors"
	"testing"

	"mediad/internal/clock"
	"mediad/internal/samplegen"
)

func twoTrackMP4Spec() samplegen.MP4Spec {
	return samplegen.MP4Spec{
		Tracks: []samplegen.MP4TrackSpec{
			{
				TrackID: 1, Timescale: 90000, Width: 1280, Height: 720,
				SampleN: 60, SampleDur: 3000, Rotation90: 1,
				ColourType: [4]byte{'n', 'c', 'l', 'x'},
				Primaries:  1, Transfer: 1, Matrix: 1, FullRange: true,
				CropX: 4, CropY: 6, CropW: 1272, CropH: 708,
			},
			{
				TrackID: 2, Timescale: 30000, Width: 640, Height: 360,
				SampleN: 30, SampleDur: 1000, Rotation90: 1,
			},
		},
	}
}

func TestParseMP4RoundTrip(t *testing.T) {
	data, err := samplegen.BuildMP4(twoTrackMP4Spec())
	if err != nil {
		t.Fatal(err)
	}
	descs, perr := ParseMP4(bytes.NewReader(data))
	if perr != nil {
		t.Fatalf("healthy MP4 must not error, got %v", perr)
	}
	if len(descs) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(descs))
	}
	d := descs[0]
	if d.Container != KindMP4 || d.TrackID != 1 || d.Timescale != 90000 {
		t.Fatalf("track identity wrong: %+v", d)
	}
	if d.Width != 1280 || d.Height != 720 {
		t.Fatalf("dimensions wrong: %dx%d", d.Width, d.Height)
	}
	if d.Rotation == nil || d.Rotation.Degrees != 90 {
		t.Fatalf("rotation not decoded: %+v", d.Rotation)
	}
	if d.Crop == nil || d.Crop.Width != 1272 || d.Crop.X != 4 {
		t.Fatalf("crop not decoded: %+v", d.Crop)
	}
	if d.Colour == nil || d.Colour.Primaries != 1 || !d.Colour.FullRange {
		t.Fatalf("colour not decoded: %+v", d.Colour)
	}
}

func TestParseMP4Truncated(t *testing.T) {
	spec := twoTrackMP4Spec()
	spec.Truncate = true
	data, err := samplegen.BuildMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	descs, perr := ParseMP4(bytes.NewReader(data))
	if perr == nil {
		t.Fatal("truncated MP4 must report an error")
	}
	var ce *clock.Error
	if !errors.As(perr, &ce) || !ce.Recoverable || ce.Stage != clock.StageMP4 {
		t.Fatalf("want recoverable mp4 error, got %v", perr)
	}
	if len(descs) < 0 {
		t.Fatal("parser must return whatever tracks completed before the cut")
	}
}

func TestParseMP4BadSampleRangeIsolated(t *testing.T) {
	spec := twoTrackMP4Spec()
	spec.BadSampleRange = true
	data, err := samplegen.BuildMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	descs, perr := ParseMP4(bytes.NewReader(data))
	if perr == nil {
		t.Fatal("illegal sample range must be reported")
	}
	var ce *clock.Error
	if !errors.As(perr, &ce) || !ce.Recoverable {
		t.Fatalf("want recoverable error, got %v", perr)
	}
	// Track 1 is corrupt; track 2 must survive the isolation.
	if len(descs) != 1 || descs[0].TrackID != 2 {
		t.Fatalf("only the healthy track must remain, got %+v", descs)
	}
}

func TestParseMXFRoundTrip(t *testing.T) {
	spec := samplegen.MXFSpec{
		Tracks: []samplegen.MXFTrackSpec{
			{
				TrackID: 7, EditRateN: 25, EditRateD: 1, Width: 1920, Height: 1080,
				Rotation90: 3, Primaries: 9, Transfer: 16, Matrix: 9, FullRange: false,
				CropX: 0, CropY: 0, CropW: 1920, CropH: 1080,
			},
		},
	}
	descs, perr := ParseMXF(samplegen.BuildMXF(spec))
	if perr != nil {
		t.Fatalf("healthy MXF must not error, got %v", perr)
	}
	if len(descs) != 1 {
		t.Fatalf("want 1 descriptor, got %d", len(descs))
	}
	d := descs[0]
	if d.Container != KindMXF || d.TrackID != 7 || d.Timescale != 25 || d.EditRateDen != 1 {
		t.Fatalf("identity/rate wrong: %+v", d)
	}
	if d.Rotation == nil || d.Rotation.Degrees != 270 {
		t.Fatalf("rotation wrong: %+v", d.Rotation)
	}
	if d.Colour == nil || d.Colour.Primaries != 9 || d.Colour.Transfer != 16 {
		t.Fatalf("colour wrong: %+v", d.Colour)
	}
	if d.Crop == nil || d.Crop.Width != 1920 {
		t.Fatalf("crop wrong: %+v", d.Crop)
	}
}

func TestParseMXFTruncated(t *testing.T) {
	spec := samplegen.MXFSpec{
		Tracks: []samplegen.MXFTrackSpec{
			{TrackID: 1, EditRateN: 25, EditRateD: 1, Width: 320, Height: 240},
			{TrackID: 2, EditRateN: 25, EditRateD: 1, Width: 320, Height: 240, Rotation90: 2},
		},
		Truncate: true,
	}
	descs, perr := ParseMXF(samplegen.BuildMXF(spec))
	if perr == nil {
		t.Fatal("truncated KLV must be reported")
	}
	var ce *clock.Error
	if !errors.As(perr, &ce) || !ce.Recoverable || ce.Stage != clock.StageMXF {
		t.Fatalf("want recoverable mxf error, got %v", perr)
	}
	if len(descs) != 1 || descs[0].TrackID != 1 {
		t.Fatalf("first track must survive truncation, got %+v", descs)
	}
}

func TestResolveMP4AndMXFEndToEnd(t *testing.T) {
	mp4Data, err := samplegen.BuildMP4(samplegen.MP4Spec{
		Tracks: []samplegen.MP4TrackSpec{{
			TrackID: 1, Timescale: 90000, Width: 1280, Height: 720,
			SampleN: 10, SampleDur: 3000, Rotation90: 1,
			ColourType: [4]byte{'n', 'c', 'l', 'x'}, Primaries: 1, Transfer: 1, Matrix: 1,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	mp4Descs, perr := ParseMP4(bytes.NewReader(mp4Data))
	if perr != nil {
		t.Fatal(perr)
	}
	mxfDescs, perr := ParseMXF(samplegen.BuildMXF(samplegen.MXFSpec{
		Tracks: []samplegen.MXFTrackSpec{{
			TrackID: 9, EditRateN: 25, EditRateD: 1, Width: 1280, Height: 720,
			Rotation90: 2, Primaries: 9, Transfer: 1, Matrix: 9,
		}},
	}))
	if perr != nil {
		t.Fatal(perr)
	}
	resolved, err := Resolve(append(mp4Descs, mxfDescs...))
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Rotation.Degrees != 180 || resolved.Rotation.Priority != PriorityMXF {
		t.Fatalf("MXF rotation must override MP4: %+v", resolved.Rotation)
	}
	if resolved.Colour.Primaries != 9 || resolved.Colour.Transfer != 1 {
		t.Fatalf("per-field colour merge wrong: %+v", resolved.Colour)
	}
}
