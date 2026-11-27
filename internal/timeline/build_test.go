package timeline

import (
	"testing"

	"mediaregress/internal/media"
	"mediaregress/internal/track"
)

func videoTrack(id string) *track.Track {
	return &track.Track{
		ID: id, Kind: track.KindVideo, Container: "mp4", Timescale: 90000,
		Duration: 400 * media.Millisecond, Version: track.ParseVersion,
		RotSource: track.Source{Container: "mp4", Element: "tkhd", Offset: 100},
		Samples: []track.Sample{
			{Index: 0, Start: 0, Duration: 100 * media.Millisecond, Offset: 10},
			{Index: 1, Start: 200 * media.Millisecond, Duration: 100 * media.Millisecond, Offset: 20},
		},
	}
}

func audioTrack(id string) *track.Track {
	return &track.Track{
		ID: id, Kind: track.KindAudio, Container: "mp4", Timescale: 48000,
		Duration: 300 * media.Millisecond, Version: track.ParseVersion,
		RotSource: track.Source{Container: "mp4", Element: "tkhd", Offset: 200},
		Samples: []track.Sample{
			{Index: 0, Start: 0, Duration: 150 * media.Millisecond, Offset: 50},
			{Index: 1, Start: 150 * media.Millisecond, Duration: 150 * media.Millisecond, Offset: 60},
		},
	}
}

func TestBuildMergesTracks(t *testing.T) {
	in := &track.Input{Tracks: []*track.Track{videoTrack("v0"), audioTrack("a0")}}
	tl, err := Build(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Tracks) != 2 || tl.Tracks[0].TrackID != "a0" {
		t.Fatalf("tracks=%v", tl.Tracks)
	}
	// The timeline end is the last sample end: video spans 0-100ms and
	// 200-300ms, so 300ms is the merged end.
	if tl.End != 300*media.Millisecond {
		t.Errorf("timeline end=%d", tl.End)
	}
	v := tl.Track("v0")
	if v == nil || v.End != 300*media.Millisecond || v.Start != 0 {
		t.Fatalf("video=%+v", v)
	}
	// Metadata origins must still reference the original element offsets.
	found := false
	for _, m := range v.Metadata {
		if m.Field == "rotation" && m.Source.Offset == 100 && m.Version == track.ParseVersion {
			found = true
		}
	}
	if !found {
		t.Errorf("rotation origin missing: %+v", v.Metadata)
	}
}

func TestBuildRejectsBackwardsSamples(t *testing.T) {
	tr := videoTrack("v0")
	tr.Samples = []track.Sample{
		{Index: 0, Start: 200 * media.Millisecond, Duration: 100 * media.Millisecond, Offset: 10},
		{Index: 1, Start: 250 * media.Millisecond, Duration: 100 * media.Millisecond, Offset: 20},
	}
	tl, err := Build(&track.Input{Tracks: []*track.Track{tr}})
	if err == nil {
		t.Fatal("expected no-usable-track error")
	}
	if len(tl.Rejected) != 1 || !tl.Rejected[0].Rejected {
		t.Fatalf("rejected=%+v", tl.Rejected)
	}
	e := tl.Rejected[0].Errors[0]
	if e.Class != media.ClassTimeRegression || e.Offset != 20 || e.Track != "v0" {
		t.Errorf("error=%+v", e)
	}
	if e.Stage != media.StageTimeline {
		t.Errorf("stage=%s", e.Stage)
	}
}

func TestBuildRejectsNegativeSample(t *testing.T) {
	tr := videoTrack("v0")
	tr.Samples = []track.Sample{{Index: 0, Start: -1, Duration: 10, Offset: 7}}
	tl, _ := Build(&track.Input{Tracks: []*track.Track{tr}})
	if len(tl.Rejected) != 1 {
		t.Fatal("negative sample not rejected")
	}
	if got := tl.Rejected[0].Errors[0]; got.Offset != 7 || got.Class != media.ClassTimeRegression {
		t.Errorf("error=%+v", got)
	}
}

func TestBuildZeroTimescale(t *testing.T) {
	tr := videoTrack("v0")
	tr.Timescale = 0
	tl, _ := Build(&track.Input{Tracks: []*track.Track{tr}})
	if len(tl.Rejected) != 1 {
		t.Fatal("zero timescale not rejected")
	}
	if got := tl.Rejected[0].Errors[0]; got.Class != media.ClassInvalidTimebase {
		t.Errorf("class=%s", got.Class)
	}
}

func TestBuildPropagatesParseRejection(t *testing.T) {
	tr := videoTrack("v0")
	tr.Rejected = true
	tr.AddError(media.NewError(media.ClassInvalidCrop, media.StageParse, "v0", 42, "bad crop"))
	tl, err := Build(&track.Input{Tracks: []*track.Track{tr, audioTrack("a0")}})
	if err != nil {
		t.Fatal(err)
	}
	if len(tl.Rejected) != 1 || len(tl.Tracks) != 1 {
		t.Fatalf("rejected=%d tracks=%d", len(tl.Rejected), len(tl.Tracks))
	}
	if tl.Rejected[0].Errors[0].Offset != 42 {
		t.Errorf("parse error lost")
	}
}

func TestBuildDeclaredDurationMismatchRecorded(t *testing.T) {
	tr := videoTrack("v0")
	tr.Duration = media.Second
	tl, err := Build(&track.Input{Tracks: []*track.Track{tr}})
	if err != nil {
		t.Fatal(err)
	}
	if tl.Tracks[0].End != 300*media.Millisecond {
		t.Fatalf("end=%d", tl.Tracks[0].End)
	}
	if len(tl.Tracks[0].Errors) == 0 {
		t.Fatal("duration mismatch not recorded")
	}
	if tl.Tracks[0].Rejected {
		t.Fatal("mismatch should be diagnostic, not a rejection")
	}
}

func TestSampleAtAndBoundaries(t *testing.T) {
	in := &track.Input{Tracks: []*track.Track{videoTrack("v0")}}
	tl, _ := Build(in)
	v := tl.Track("v0")
	if s, ok := v.SampleAt(200 * media.Millisecond); !ok || s.Index != 1 {
		t.Fatalf("sample=%+v ok=%v", s, ok)
	}
	if _, ok := v.SampleAt(150 * media.Millisecond); ok {
		t.Fatal("gap instant must not resolve")
	}
	b := v.SampleBoundaries(0, 300*media.Millisecond)
	if len(b) != 2 || b[0] != 0 || b[1] != 200*media.Millisecond {
		t.Fatalf("boundaries=%v", b)
	}
	// A range starting inside a sample clamps to the range start.
	b = v.SampleBoundaries(50*media.Millisecond, 300*media.Millisecond)
	if len(b) != 2 || b[0] != 50*media.Millisecond {
		t.Fatalf("clamped boundaries=%v", b)
	}
}

func TestBuildNilInput(t *testing.T) {
	_, err := Build(nil)
	if !media.IsClass(err, media.ClassDependency) {
		t.Fatalf("err=%v", err)
	}
}

func TestErrorsByTrack(t *testing.T) {
	tr := videoTrack("v0")
	tr.Samples = []track.Sample{{Index: 0, Start: 5, Duration: 10, Offset: 3}}
	tr.Duration = media.Second
	tl, _ := Build(&track.Input{Tracks: []*track.Track{tr}})
	_ = tl.ErrorsByTrack()
}
