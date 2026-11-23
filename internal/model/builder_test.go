package model

import (
	"testing"

	"mediaforensics/internal/clock"
)

func sample(id, idx int, start, dur int64, off int64) Sample {
	return Sample{
		TrackID: id, Index: idx, Offset: off, Size: 16,
		Start: clock.New(start, 1), Duration: clock.New(dur, 1),
		Keyframe: true, Confidence: ConfidenceHigh, Basis: "stbl",
	}
}

func TestBuildNormalizesTimescale(t *testing.T) {
	// 90 ticks at 90kHz video vs 48 ticks at 48kHz audio: both 1 second.
	video := &Track{ID: 1, Kind: KindVideo, Timescale: 90000, EarliestOffset: 100,
		Samples: []Sample{sample(1, 0, 0, 45000, 100), sample(1, 1, 45000, 45000, 200)},
		Clips:   []Clip{{TrackID: 1, FromSample: 0, ToSample: 2}}}
	audio := &Track{ID: 2, Kind: KindAudio, Timescale: 48000, EarliestOffset: 300,
		Samples: []Sample{sample(2, 0, 0, 48000, 300)},
		Clips:   []Clip{{TrackID: 2, FromSample: 0, ToSample: 1}}}
	tl := Build([]*Track{video, audio})
	if len(tl.Errors) != 0 {
		t.Fatalf("unexpected errors: %v", tl.Errors)
	}
	if tl.Duration.String() != "1/1" {
		t.Fatalf("duration = %s, want 1/1", tl.Duration)
	}
	if video.Samples[0].Start.String() != "0/1" || video.Samples[1].Start.String() != "1/2" {
		t.Fatalf("video starts not normalized: %s %s", video.Samples[0].Start, video.Samples[1].Start)
	}
	placed, err := Schedule(video)
	if err != nil || len(placed) != 2 || placed[1].At.String() != "1/2" {
		t.Fatalf("schedule = %+v err=%v", placed, err)
	}
}

func TestNonZeroStartAndOverlappingClipsKept(t *testing.T) {
	tr := &Track{ID: 1, Kind: KindVideo, Timescale: 10, EarliestOffset: 10,
		Samples: []Sample{sample(1, 0, 0, 10, 10), sample(1, 1, 10, 10, 20)},
		Clips: []Clip{
			{TrackID: 1, FromSample: 0, ToSample: 2, TrackStart: clock.New(5, 1), TrackOffset: clock.New(1, 1)},
			{TrackID: 1, FromSample: 0, ToSample: 1, TrackStart: clock.New(6, 1)},
		}}
	Build([]*Track{tr})
	placed, err := Schedule(tr)
	if err != nil {
		t.Fatal(err)
	}
	if len(placed) != 3 {
		t.Fatalf("overlapping clips must both be retained, got %d placements", len(placed))
	}
	// First placement: track start 5 ticks at timescale 10 -> 1/2s, plus
	// composition offset 1 tick -> 1/10s: 1/2 + 1/10 = 3/5s.
	if placed[0].At.String() != "3/5" {
		t.Fatalf("non-zero composition offset lost: %s", placed[0].At)
	}
}

func TestZeroLengthClipEmitsNothing(t *testing.T) {
	tr := &Track{ID: 1, Kind: KindVideo, Timescale: 10, EarliestOffset: 0,
		Samples: []Sample{sample(1, 0, 0, 10, 0)},
		Clips:   []Clip{{TrackID: 1, FromSample: 1, ToSample: 1}}}
	Build([]*Track{tr})
	placed, err := Schedule(tr)
	if err != nil || len(placed) != 0 || !tr.Clips[0].ZeroLength {
		t.Fatalf("zero-length clip: placed=%d err=%v", len(placed), err)
	}
}

func TestOverflowBlocksTrackAndKeepsFirstOffset(t *testing.T) {
	// Normalized clip start is MaxInt64 seconds; adding the sample duration
	// during scheduling must overflow instead of wrapping.
	huge := clock.New(1<<63-1, 1)
	tr := &Track{ID: 7, Kind: KindVideo, Timescale: 1, EarliestOffset: 4242,
		Samples: []Sample{{TrackID: 7, Index: 0, Offset: 4242, Size: 1,
			Start: clock.Zero, Duration: clock.New(1, 1), Confidence: ConfidenceHigh}},
		Clips: []Clip{{TrackID: 7, FromSample: 0, ToSample: 1, TrackStart: huge}}}
	tl := Build([]*Track{tr})
	// Build's duration scan hits the same overflow first; either way the
	// track must end up blocked with its first source offset preserved.
	if !tr.Blocked {
		if _, err := Schedule(tr); err == nil {
			t.Fatal("scheduling past MaxInt64 must overflow")
		}
	}
	if tr.EarliestOffset != 4242 {
		t.Fatalf("first source offset not preserved: %d", tr.EarliestOffset)
	}
	if len(tl.Errors) == 0 {
		t.Fatal("overflow error must be propagated")
	}
	for _, e := range tl.Errors {
		if e.Offset != 4242 || e.Stage != StageClock {
			t.Fatalf("error lost offset/stage: %+v", e)
		}
	}
	if _, err := Schedule(tr); err == nil {
		t.Fatal("scheduling a blocked track must fail")
	}
}

func TestValidSamplesExcludesDamaged(t *testing.T) {
	tr := &Track{ID: 1, Kind: KindVideo, Timescale: 10,
		Samples: []Sample{
			{Index: 0, Confidence: ConfidenceHigh},
			{Index: 1, Confidence: ConfidenceLow, Basis: "adjacent"},
			{Index: 2, Confidence: ConfidenceMedium},
			{Index: 3, Confidence: ConfidenceHigh, Corrupt: true},
		}}
	got := tr.ValidSamples()
	if len(got) != 2 || got[0].Index != 0 || got[1].Index != 2 {
		t.Fatalf("valid sample filter = %+v", got)
	}
}
