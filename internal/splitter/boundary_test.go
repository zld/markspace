package splitter

import (
	"testing"

	"mediad/internal/mp4"
	"mediad/internal/timeline"
)

func TestSplitNearestSampleBoundary(t *testing.T) {
	// 5 samples of uneven (VFR) duration in microseconds-equivalent ticks.
	samples := []MappedSample{
		{TrackID: 1, Index: 0, StartUS: 0, EndUS: 1000, Offset: 0, Size: 4},
		{TrackID: 1, Index: 1, StartUS: 1000, EndUS: 3000, Offset: 4, Size: 4},
		{TrackID: 1, Index: 2, StartUS: 3000, EndUS: 3500, Offset: 8, Size: 4},
		{TrackID: 1, Index: 3, StartUS: 3500, EndUS: 6000, Offset: 12, Size: 4},
		{TrackID: 1, Index: 4, StartUS: 6000, EndUS: 6200, Offset: 16, Size: 4},
	}
	// Target 3000us. First boundary lands exactly on sample 2's start.
	cfg := Config{TargetSegmentUS: 3000, Timescales: map[uint32]uint32{1: 1_000_000}}
	segs := Split(1, samples, cfg)
	if len(segs) != 3 {
		t.Fatalf("want 3 segments (final partial), got %d: %+v", len(segs), segs)
	}
	if segs[0].EndUS != 3000 || segs[0].AdjustUS != 0 {
		t.Fatalf("first boundary not exact: %+v", segs[0])
	}
	if segs[1].StartUS != 3000 || segs[1].InputOffset != 8 {
		t.Fatalf("second segment must start at real sample offset 8: %+v", segs[1])
	}
	if segs[2].StartUS != 6000 {
		t.Fatalf("third segment must start at the 6000us sample: %+v", segs[2])
	}
}

func TestSplitSnapsToNearest(t *testing.T) {
	// Requested boundary 2200: at the crossing sample start 3000, candidates
	// are current end 1000 (gap 1200) and 3000 (gap 800) -> snap to 3000.
	// Next requested boundary 5200: crossing at 6000; candidates 3500
	// (gap 1700) and 6000 (gap 800) -> snap to 6000.
	samples := []MappedSample{
		{Index: 0, StartUS: 0, EndUS: 1000, Offset: 0},
		{Index: 1, StartUS: 1000, EndUS: 3000, Offset: 4},
		{Index: 2, StartUS: 3000, EndUS: 3500, Offset: 8},
		{Index: 3, StartUS: 3500, EndUS: 6000, Offset: 12},
		{Index: 4, StartUS: 6000, EndUS: 9000, Offset: 16},
	}
	cfg := Config{TargetSegmentUS: 2200, Timescales: map[uint32]uint32{1: 1_000_000}}
	segs := Split(1, samples, cfg)
	if len(segs) != 3 {
		t.Fatalf("want 3 segments, got %d: %+v", len(segs), segs)
	}
	// Seg0 requested end 2200, snapped forward to nearer edge 3000 -> +800.
	if segs[0].EndUS != 3000 || segs[0].AdjustUS != 800 {
		t.Fatalf("seg0 snap wrong: %+v", segs[0])
	}
	// Seg1 starts at 3000; requested end 5200, nearest real edge 6000.
	if segs[1].StartUS != 3000 || segs[1].EndUS != 6000 || segs[1].AdjustUS != 800 {
		t.Fatalf("seg1 wrong: %+v", segs[1])
	}
	// Final partial segment.
	if segs[2].StartUS != 6000 || segs[2].EndUS != 9000 {
		t.Fatalf("seg2 wrong: %+v", segs[2])
	}
}

func TestSplitSnapsBackwardWhenCloser(t *testing.T) {
	// Requested boundary 2050: the crossing sample starts at 2600 (gap 550),
	// while the previous sample's tail is 1900 (gap 150) -> snap backward.
	samples := []MappedSample{
		{Index: 0, StartUS: 0, EndUS: 1900, Offset: 0},
		{Index: 1, StartUS: 1900, EndUS: 2600, Offset: 4},
		{Index: 2, StartUS: 2600, EndUS: 5000, Offset: 8},
		{Index: 3, StartUS: 5000, EndUS: 7000, Offset: 12},
	}
	cfg := Config{TargetSegmentUS: 2050, Timescales: map[uint32]uint32{1: 1_000_000}}
	segs := Split(1, samples, cfg)
	if segs[0].EndUS != 1900 || segs[0].AdjustUS != -150 {
		t.Fatalf("backward snap wrong: %+v", segs[0])
	}
	if segs[1].StartUS != 1900 {
		t.Fatalf("next segment start wrong: %+v", segs[1])
	}
}

func TestSplitFlagsInvalidIntervals(t *testing.T) {
	samples := []MappedSample{
		{Index: 0, StartUS: 0, EndUS: 1000},
		{Index: 1, StartUS: 1000, EndUS: 2000, Invalid: true, ErrMsg: "rollback"},
		{Index: 2, StartUS: 2000, EndUS: 4000},
	}
	cfg := Config{TargetSegmentUS: 2000, Timescales: map[uint32]uint32{1: 1_000_000}}
	segs := Split(1, samples, cfg)
	if !segs[0].SpansInvalid {
		t.Fatalf("segment containing invalid sample must be flagged: %+v", segs[0])
	}
}

func TestMapTrackTimescaleConversion(t *testing.T) {
	// timescale 1000: 1000 ticks = 1s = 1_000_000us. Two samples.
	data := mp4.BuildTestFragment(1, 0, 8)
	r := mp4.NewSeeker(data)
	boxes, _ := mp4.ScanTopLevel(r, "f.m4s", mp4.Limits{})
	frag, err := mp4.DecodeFragment(r, "f.m4s", boxes[0], boxes[1], mp4.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	// Replace samples to two 1000-tick samples for a deterministic check.
	tf := frag.Tracks[0]
	tf.Samples = []mp4.Sample{
		{Index: 0, DataOffset: 0, Size: 4, Duration: 1000},
		{Index: 1, DataOffset: 4, Size: 4, Duration: 1000},
	}
	clock := timeline.NewTrackClock(timeline.ClockConfig{TrackID: 1, Timescale: 1000})
	ms := MapTrack(clock, tf, frag)
	if ms[0].StartUS != 0 || ms[0].EndUS != 1_000_000 || ms[1].StartUS != 1_000_000 {
		t.Fatalf("timescale mapping wrong: %+v", ms)
	}
}

func TestMapTrackAcrossRolloverStaysMonotonic(t *testing.T) {
	data := mp4.BuildTestFragment(1, 0, 8)
	r := mp4.NewSeeker(data)
	boxes, _ := mp4.ScanTopLevel(r, "f.m4s", mp4.Limits{})
	frag, _ := mp4.DecodeFragment(r, "f.m4s", boxes[0], boxes[1], mp4.Limits{})
	clock := timeline.NewTrackClock(timeline.ClockConfig{TrackID: 1, Timescale: 90000})

	// Fragment 1: base in the top half of the 32-bit window; the second
	// sample's base crosses 2^32-1 so the next fragment's tfdt wraps to zero.
	tf1 := *frag.Tracks[0]
	tf1.BaseMediaT = 0xFFFFFFE0
	tf1.Samples = []mp4.Sample{
		{Index: 0, DataOffset: 0, Size: 4, Duration: 16},
		{Index: 1, DataOffset: 4, Size: 4, Duration: 16},
	}
	ms1 := MapTrack(clock, &tf1, frag)

	// Fragment 2: tfdt wrapped back near zero. Same clock must absorb rollover.
	frag2 := *frag
	frag2.SeqNum = 2
	tf2 := *frag.Tracks[0]
	tf2.BaseMediaT = 0
	tf2.Samples = []mp4.Sample{{Index: 0, DataOffset: 8, Size: 4, Duration: 16}}
	ms2 := MapTrack(clock, &tf2, &frag2)

	if ms1[1].EndUS > ms2[0].StartUS {
		t.Fatalf("cross-fragment rollover broke monotonicity: %d > %d",
			ms1[1].EndUS, ms2[0].StartUS)
	}
	if ms2[0].AdjustUS <= 0 || ms2[0].Invalid {
		t.Fatalf("wrapped fragment must carry a rollover adjustment: %+v", ms2[0])
	}
	if clock.Rollovers() != 1 {
		t.Fatalf("rollovers = %d, want 1", clock.Rollovers())
	}
}
