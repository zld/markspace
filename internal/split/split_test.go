package split

import (
	"testing"

	"mediaregress/internal/media"
	"mediaregress/internal/timeline"
	"mediaregress/internal/track"
)

func makeTimeline(samples []track.Sample) *timeline.Timeline {
	tr := &timeline.TrackTimeline{
		TrackID: "v0", Kind: track.KindVideo, Timescale: 90000,
		ParseVersion: track.ParseVersion, Samples: samples,
	}
	return &timeline.Timeline{Start: 0, End: samples[len(samples)-1].End(), Tracks: []*timeline.TrackTimeline{tr}}
}

func uniform(n int, dur media.Ticks) []track.Sample {
	out := make([]track.Sample, n)
	for i := range out {
		out[i] = track.Sample{Index: i, Start: media.Ticks(i) * dur, Duration: dur, Size: 10, Offset: int64(i * 10), Keyframe: i == 0}
	}
	return out
}

func TestTrackCutsAtSampleBoundaries(t *testing.T) {
	// 20 samples of 100ms each with a 1s target: one cut, at sample 10.
	tl := makeTimeline(uniform(20, 100*media.Millisecond))
	segs, err := Tracks(tl, media.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 2 {
		t.Fatalf("segments=%d want 2", len(segs))
	}
	if segs[0].Window.Start != 0 || segs[0].Window.End != media.Second {
		t.Errorf("seg0 window=%+v", segs[0].Window)
	}
	if segs[1].Window.Start != media.Second || segs[1].Window.End != 2*media.Second {
		t.Errorf("seg1 window=%+v", segs[1].Window)
	}
	// The sample starting exactly on the cut belongs to segment 1 only.
	if segs[1].StartSample != 10 || segs[0].EndSample != 10 {
		t.Errorf("ownership: seg0 end=%d seg1 start=%d", segs[0].EndSample, segs[1].StartSample)
	}
	if err := Validate(segs); err != nil {
		t.Fatal(err)
	}
}

func TestBoundarySampleOwnedOnce(t *testing.T) {
	tl := makeTimeline(uniform(6, 250*media.Millisecond))
	segs, err := Tracks(tl, 500*media.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[int]int{}
	for _, s := range segs {
		for i := s.StartSample; i < s.EndSample; i++ {
			counts[i]++
		}
	}
	for i := 0; i < 6; i++ {
		if counts[i] != 1 {
			t.Errorf("sample %d owned %d times", i, counts[i])
		}
	}
	if len(segs) != 3 {
		t.Fatalf("segments=%d want 3", len(segs))
	}
}

func TestIrregularSampleSpacing(t *testing.T) {
	// A gap means the cut candidate snaps forward to the next sample start.
	samples := []track.Sample{
		{Index: 0, Start: 0, Duration: 100 * media.Millisecond, Size: 4, Offset: 0},
		{Index: 1, Start: 100 * media.Millisecond, Duration: 100 * media.Millisecond, Size: 4, Offset: 4},
		{Index: 2, Start: 600 * media.Millisecond, Duration: 100 * media.Millisecond, Size: 4, Offset: 8},
		{Index: 3, Start: 700 * media.Millisecond, Duration: 100 * media.Millisecond, Size: 4, Offset: 12},
	}
	segs, err := Tracks(makeTimeline(samples), media.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 {
		t.Fatalf("segments=%d", len(segs))
	}
	// No candidate exists inside [0, 0.8s) at or after 0.8s that advances, so
	// the single segment covers the whole track.
	if segs[0].Window.End != 800*media.Millisecond {
		t.Errorf("end=%d", segs[0].Window.End)
	}
}

func TestSplitShortTrack(t *testing.T) {
	tl := makeTimeline(uniform(2, 100*media.Millisecond))
	segs, err := Tracks(tl, media.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(segs) != 1 || segs[0].SampleCount != 2 {
		t.Fatalf("segs=%+v", segs)
	}
	if segs[0].Window.End != 200*media.Millisecond {
		t.Errorf("end=%d", segs[0].Window.End)
	}
}

func TestSplitRejectsOverlap(t *testing.T) {
	samples := []track.Sample{
		{Index: 0, Start: 0, Duration: 200 * media.Millisecond, Offset: 0},
		{Index: 1, Start: 100 * media.Millisecond, Duration: 100 * media.Millisecond, Offset: 10},
	}
	_, err := Track(&timeline.TrackTimeline{TrackID: "v0", Samples: samples}, media.Second)
	if !media.IsClass(err, media.ClassTimeRegression) {
		t.Fatalf("err=%v", err)
	}
	e, _ := media.AsError(err)
	if e.Offset != 10 || e.Track != "v0" {
		t.Errorf("error=%+v", e)
	}
}

func TestSplitNoSamples(t *testing.T) {
	_, err := Track(&timeline.TrackTimeline{TrackID: "v0"}, media.Second)
	if !media.IsClass(err, media.ClassCorruptInput) {
		t.Fatalf("err=%v", err)
	}
}

func TestSplitRejectsZeroTarget(t *testing.T) {
	_, err := Track(&timeline.TrackTimeline{TrackID: "v0", Samples: uniform(1, 10)}, 0)
	if !media.IsClass(err, media.ClassInvalidTimebase) {
		t.Fatalf("err=%v", err)
	}
}

func TestSegmentsSortedPerTrack(t *testing.T) {
	trA := &timeline.TrackTimeline{TrackID: "a0", Kind: track.KindAudio,
		Samples: []track.Sample{{Index: 0, Start: 0, Duration: 200 * media.Millisecond, Size: 1}}}
	trV := &timeline.TrackTimeline{TrackID: "v0", Kind: track.KindVideo,
		Samples: uniform(10, 100*media.Millisecond)}
	tl := &timeline.Timeline{Tracks: []*timeline.TrackTimeline{trV, trA}}
	segs, err := Tracks(tl, 500*media.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if segs[0].Track != "a0" {
		t.Fatalf("first track=%s", segs[0].Track)
	}
	if err := Validate(segs); err != nil {
		t.Fatal(err)
	}
}

func TestSegmentSourceOffsetAndSize(t *testing.T) {
	tl := makeTimeline(uniform(4, 250*media.Millisecond))
	segs, _ := Tracks(tl, 500*media.Millisecond)
	if segs[0].SourceOffset != 0 || segs[0].Size != 20 {
		t.Errorf("seg0 off=%d size=%d", segs[0].SourceOffset, segs[0].Size)
	}
	if segs[1].SourceOffset != 20 || segs[1].Size != 20 {
		t.Errorf("seg1 off=%d size=%d", segs[1].SourceOffset, segs[1].Size)
	}
	_ = segs
}

func TestOwnerLookup(t *testing.T) {
	tl := makeTimeline(uniform(10, 100*media.Millisecond))
	segs, _ := Tracks(tl, 500*media.Millisecond)
	if s, ok := Owner(segs, "v0", 4); !ok || s.Index != 0 {
		t.Errorf("owner=%+v", s)
	}
	if s, ok := Owner(segs, "v0", 5); !ok || s.Index != 1 {
		t.Errorf("owner=%+v", s)
	}
}

func TestValidateDetectsGap(t *testing.T) {
	segs := []Segment{
		{ID: "v0/0000", Track: "v0", Window: media.Window{Start: 0, End: 100}, StartSample: 0, EndSample: 2},
		{ID: "v0/0001", Track: "v0", Window: media.Window{Start: 100, End: 200}, StartSample: 3, EndSample: 4},
	}
	if err := Validate(segs); err == nil {
		t.Fatal("sample gap not detected")
	}
}
