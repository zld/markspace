package index

import (
	"testing"

	"medcheck/internal/model"
)

func kf(track, source, seq uint32, micros int64, off int64, valid bool, ftype model.FrameType) *model.Frame {
	o := model.RawOffset{Stream: "s", Start: off, End: off + 4}
	f := &model.Frame{
		Track:     track,
		Source:    source,
		Seq:       seq,
		EventTime: model.Timestamp{Micros: micros, HasMicros: true, Offset: o, Base: model.Rational{Num: 1e6, Den: 1}},
		Type:      ftype,
		Offset:    o,
		Valid:     valid,
	}
	if !valid {
		f.Err = model.ChecksumError(o, track, source, seq)
	}
	return f
}

func TestBuildExcludesInvalidAndDelta(t *testing.T) {
	frames := []*model.Frame{
		kf(1, 1, 0, 0, 0, true, model.TypeKeyFrame),
		kf(1, 1, 1, 10, 4, true, model.TypeDelta),     // delta excluded
		kf(1, 1, 2, 20, 8, false, model.TypeKeyFrame), // invalid excluded
		kf(1, 1, 3, 30, 12, true, model.TypeKeyFrame),
	}
	idx := Build(frames)
	if idx.Len() != 2 {
		t.Fatalf("index len = %d want 2", idx.Len())
	}
	for _, e := range idx.Entries() {
		if e.Offset.Start < 0 {
			t.Fatal("entry must carry original offset")
		}
	}
}

func TestAtOrBeforeQuery(t *testing.T) {
	frames := []*model.Frame{
		kf(1, 1, 0, 0, 0, true, model.TypeKeyFrame),
		kf(1, 1, 1, 100, 4, true, model.TypeKeyFrame),
		kf(1, 1, 2, 200, 8, true, model.TypeKeyFrame),
	}
	idx := Build(frames)
	if _, ok := idx.AtOrBefore(1, -1); ok {
		t.Fatal("frame before the first key must not hit")
	}
	e, ok := idx.AtOrBefore(1, 50)
	if !ok || e.Micros != 0 || e.Offset.Start != 0 {
		t.Fatalf("AtOrBefore(50) = %+v,%v want keyframe at 0", e, ok)
	}
	e, ok = idx.AtOrBefore(1, 99)
	if !ok || e.Micros != 0 || e.Offset.Start != 0 {
		t.Fatalf("AtOrBefore(99) = %+v,%v", e, ok)
	}
	e, ok = idx.AtOrBefore(1, 150)
	if !ok || e.Micros != 100 {
		t.Fatalf("AtOrBefore(150) = %+v,%v", e, ok)
	}
	if _, ok := idx.AtOrBefore(2, 1000); ok {
		t.Fatal("other track must not hit")
	}
}

func TestExactHitReturnsTrackTimeOffset(t *testing.T) {
	idx := Build([]*model.Frame{kf(7, 3, 9, 250, 88, true, model.TypeKeyFrame)})
	e, ok := idx.Exact(7, 250)
	if !ok || e.Track != 7 || e.Source != 3 || e.Micros != 250 || e.Offset.Start != 88 {
		t.Fatalf("exact hit = %+v,%v", e, ok)
	}
}

func TestIntervals(t *testing.T) {
	frames := []*model.Frame{
		kf(1, 1, 0, 0, 0, true, model.TypeKeyFrame),
		kf(1, 1, 1, 100, 4, true, model.TypeKeyFrame),
		kf(1, 1, 2, 250, 8, true, model.TypeKeyFrame),
	}
	gaps := Build(frames).Intervals()
	if len(gaps) != 2 || gaps[0].GapMicros != 100 || gaps[1].GapMicros != 150 {
		t.Fatalf("intervals = %+v", gaps)
	}
}

func TestIntervalsDoNotCrossTracks(t *testing.T) {
	frames := []*model.Frame{
		kf(1, 1, 0, 0, 0, true, model.TypeKeyFrame),
		kf(2, 1, 0, 5, 4, true, model.TypeKeyFrame),
		kf(2, 1, 1, 9, 8, true, model.TypeKeyFrame),
	}
	gaps := Build(frames).Intervals()
	if len(gaps) != 1 || gaps[0].Track != 2 || gaps[0].GapMicros != 4 {
		t.Fatalf("cross-track interval leaked: %+v", gaps)
	}
}

func TestDeterministicOrder(t *testing.T) {
	a := Build([]*model.Frame{
		kf(1, 1, 0, 200, 0, true, model.TypeKeyFrame),
		kf(1, 1, 1, 100, 4, true, model.TypeKeyFrame),
	}).Entries()
	if a[0].Micros != 100 || a[1].Micros != 200 {
		t.Fatalf("not sorted: %+v", a)
	}
}
