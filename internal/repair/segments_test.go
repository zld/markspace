package repair

import (
	"testing"

	"medcheck/internal/model"
)

func fr(track, source, seq uint32, micros int64, start int64, valid bool) *model.Frame {
	o := model.RawOffset{Stream: "s", Start: start, End: start + 4}
	f := &model.Frame{
		Track:     track,
		Source:    source,
		Seq:       seq,
		EventTime: model.Timestamp{Micros: micros, HasMicros: true, Offset: o},
		Offset:    o,
		Valid:     valid,
	}
	if !valid {
		f.Err = model.ChecksumError(o, track, source, seq)
	}
	return f
}

func TestValidFrameSplitsRuns(t *testing.T) {
	ordered := []*model.Frame{
		fr(1, 1, 0, 0, 0, false),
		fr(1, 1, 1, 10, 4, false),
		fr(1, 1, 2, 20, 8, true), // valid closes the run
		fr(1, 1, 3, 30, 12, false),
	}
	segs := Build(ordered)
	if len(segs) != 2 {
		t.Fatalf("segments = %d want 2", len(segs))
	}
	if len(segs[0].Frames) != 2 || len(segs[1].Frames) != 1 {
		t.Fatalf("frame counts = %d,%d", len(segs[0].Frames), len(segs[1].Frames))
	}
	// The valid frame's offset must fall strictly between the two segments.
	if !(segs[0].EndOffset <= 8 && segs[1].StartOffset >= 12) {
		t.Fatalf("valid frame merged into damage: %+v %+v", segs[0], segs[1])
	}
	if segs[0].FirstPos != 0 {
		t.Fatalf("FirstPos = %d want 0", segs[0].FirstPos)
	}
}

func TestSegmentsDoNotCrossTracks(t *testing.T) {
	ordered := []*model.Frame{
		fr(1, 1, 0, 0, 0, false),
		fr(2, 1, 0, 5, 4, false), // different track: separate segment
	}
	segs := Build(ordered)
	if len(segs) != 2 || segs[0].Track == segs[1].Track {
		t.Fatalf("cross-track merge: %+v", segs)
	}
}

func TestSourceSwitchSeparates(t *testing.T) {
	ordered := []*model.Frame{
		fr(1, 1, 0, 0, 0, false),
		fr(1, 2, 0, 1, 4, false),
	}
	segs := Build(ordered)
	if len(segs) != 2 {
		t.Fatalf("source switch segments = %d want 2", len(segs))
	}
}

func TestSegmentHashStableAcrossOrder(t *testing.T) {
	// Same damaged frames, one boundary, supplied in two arrival orders.
	a := []*model.Frame{
		fr(1, 1, 0, 0, 0, false),
		fr(1, 1, 1, 10, 4, false),
		fr(1, 1, 2, 20, 8, false),
	}
	b := []*model.Frame{a[2], a[0], a[1]}
	sa, sb := Build(a), Build(b)
	if len(sa) != 1 || len(sb) != 1 {
		t.Fatalf("segment counts = %d,%d", len(sa), len(sb))
	}
	if sa[0].SHA256 == "" || sa[0].SHA256 != sb[0].SHA256 {
		t.Fatalf("hash not order-stable: %s vs %s", sa[0].SHA256, sb[0].SHA256)
	}
}

func TestVerifyOffsetsRoundTrip(t *testing.T) {
	set := []*model.Frame{
		fr(1, 1, 0, 0, 100, false),
		fr(1, 1, 1, 10, 200, false),
		fr(1, 1, 2, 20, 300, true),
	}
	trace := NewOffsetMap(set)
	segs := Build(set)
	if _, _, ok := VerifyOffsets(segs, trace); !ok {
		t.Fatal("all damaged frames must map back to their original offsets")
	}
	if trace.Len() != 3 {
		t.Fatalf("trace len = %d", trace.Len())
	}
}

func TestNoDamageNoSegments(t *testing.T) {
	set := []*model.Frame{fr(1, 1, 0, 0, 0, true), fr(1, 1, 1, 10, 4, true)}
	if len(Build(set)) != 0 {
		t.Fatal("clean input must produce no segments")
	}
}
