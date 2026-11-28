package media

import (
	"testing"

	"mediad/fixture"
	"mediad/timeline"
)

// makeTrack builds a video Track with keyframes every gop samples.
func makeTrack(t *testing.T, id uint32, timescale uint32, n, gop int) Track {
	t.Helper()
	samples := make([]fixture.Sample, n)
	for i := range samples {
		samples[i] = fixture.Sample{
			Data:          []byte{byte(i), 0x11, 0x22},
			DurationTicks: timescale / 30,
			Sync:          i%gop == 0,
		}
	}
	clip := fixture.BuildClip([]fixture.TrackSpec{
		{ID: id, Handler: "vide", Timescale: timescale, Width: 640, Height: 360, Samples: samples},
	})
	c, err := Parse("t.mp4", clip.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	track, _ := c.Track(id)
	return track
}

func TestAlignSegmentsAcrossRenditions(t *testing.T) {
	// Two renditions, both 30 fps with a keyframe every 30 samples (1 s).
	low := makeTrack(t, 1, 90000, 90, 30)
	high := makeTrack(t, 2, 90000, 90, 30)
	res, err := AlignSegments([]Rendition{
		{Name: "360p", Track: low},
		{Name: "1080p", Track: high},
	}, AlignOptions{TargetSegmentUs: 1_000_000, MaxBoundaryErrorUs: 34_000})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Segments) != 3 { // cuts at 0, 1 s, 2 s
		t.Fatalf("segments = %d, want 3", len(res.Segments))
	}
	for i, seg := range res.Segments {
		if seg.Start != int64(i)*1_000_000 {
			t.Fatalf("segment %d start %d", i, seg.Start)
		}
		if len(seg.ByRendition) != 2 {
			t.Fatalf("segment %d aligned renditions = %d", i, len(seg.ByRendition))
		}
		for name, rs := range seg.ByRendition {
			// Every rendition's keyframe must sit within one frame of the cut.
			if rs.BoundaryErrorUs > 34_000 {
				t.Fatalf("segment %d rendition %s boundary error %d µs", i, name, rs.BoundaryErrorUs)
			}
		}
	}
}

func TestAlignSegmentsFailsWhenRenditionLacksBoundary(t *testing.T) {
	// The low rendition has a keyframe only at the very start; the high one
	// has keyframes every second. At the 1 s cut the low rendition has no
	// keyframe within one frame.
	low := makeTrack(t, 1, 90000, 60, 60)  // single keyframe at 0
	high := makeTrack(t, 2, 90000, 90, 30) // keyframes at 0, 1 s, 2 s
	res, err := AlignSegments([]Rendition{
		{Name: "low", Track: low},
		{Name: "high", Track: high},
	}, AlignOptions{TargetSegmentUs: 1_000_000, MaxBoundaryErrorUs: 34_000})
	if err == nil {
		t.Fatal("alignment must fail when a rendition lacks a cut boundary")
	}
	if !res.Issues.HasErrors() {
		t.Fatal("the missing boundary must be a recorded issue")
	}
	found := false
	for _, is := range res.Issues {
		if is.Code == timeline.CodeNoBoundary && is.TrackID == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("issue should name the low rendition (track 1): %v", res.Issues.Strings())
	}
}

func TestAlignSegmentsChoosesDensestMasterDeterministically(t *testing.T) {
	a := makeTrack(t, 1, 90000, 90, 30)
	b := makeTrack(t, 2, 90000, 90, 15) // denser keyframes
	// Order must not affect the chosen master.
	res1, err := AlignSegments([]Rendition{{Name: "a", Track: a}, {Name: "b", Track: b}}, AlignOptions{TargetSegmentUs: 1_000_000, MaxBoundaryErrorUs: 34_000})
	if err != nil {
		t.Fatal(err)
	}
	res2, err := AlignSegments([]Rendition{{Name: "b", Track: b}, {Name: "a", Track: a}}, AlignOptions{TargetSegmentUs: 1_000_000, MaxBoundaryErrorUs: 34_000})
	if err != nil {
		t.Fatal(err)
	}
	if res1.Master != "b" || res2.Master != "b" {
		t.Fatalf("master a=%q b=%q, want b", res1.Master, res2.Master)
	}
}

func TestAlignRejectsMissingKeyframes(t *testing.T) {
	// Every sample is a non-sync sample, so there is no cut to make.
	tr := makeTrack(t, 1, 90000, 10, 30)
	for i := range tr.Samples {
		tr.Samples[i].Sync = false
	}
	_, err := AlignSegments([]Rendition{{Name: "x", Track: tr}},
		AlignOptions{TargetSegmentUs: 1_000_000, MaxBoundaryErrorUs: 1})
	if err == nil {
		t.Fatal("a master with no keyframes must fail")
	}
	if timeline.CodeOf(err) != timeline.CodeMissingKeyframe {
		t.Fatalf("code = %q", timeline.CodeOf(err))
	}
}

func TestSamplePath(t *testing.T) {
	tr := makeTrack(t, 1, 90000, 5, 5)
	start, end, err := SamplePath(tr, 1, 3)
	if err != nil {
		t.Fatal(err)
	}
	if start != tr.Samples[1].Offset {
		t.Fatalf("start = %d", start)
	}
	if end != tr.Samples[3].Offset+tr.Samples[3].Size {
		t.Fatalf("end = %d", end)
	}
	if _, _, err := SamplePath(tr, 0, 9); err == nil {
		t.Fatal("out-of-range must error")
	}
}
