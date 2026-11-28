package timeline

import (
	"testing"

	"medcheck/internal/model"
)

func mkFrame(track, source, seq uint32, micros int64, start int64, valid bool) *model.Frame {
	off := model.RawOffset{Stream: "s", Start: start, End: start + 4}
	f := &model.Frame{
		Track:  track,
		Source: source,
		Seq:    seq,
		EventTime: model.Timestamp{
			Micros: micros, HasMicros: true, Offset: off,
			Base: model.Rational{Num: 1_000_000, Den: 1},
		},
		Offset: off,
		Valid:  valid,
	}
	if !valid {
		f.Err = model.ChecksumError(off, track, source, seq)
	}
	return f
}

func TestWatermarkAdvancesWithinLateWindow(t *testing.T) {
	e := NewEngine(3)
	e.Push(mkFrame(1, 1, 0, 10, 0, true))
	if e.View().WatermarkMicros != 7 { // 10 - 3
		t.Fatalf("wm = %d want 7", e.View().WatermarkMicros)
	}
	// Late but inside window: event time 8 >= wm 7.
	re, aff := e.Push(mkFrame(1, 1, 1, 8, 4, true))
	if !re || aff.Start != 4 {
		t.Fatalf("late in-window frame must trigger recompute: %v %+v", re, aff)
	}
}

func TestOutOfWindowFrameDoesNotMoveWatermark(t *testing.T) {
	e := NewEngine(3)
	e.Push(mkFrame(1, 1, 0, 10, 0, true))
	before := e.View().WatermarkMicros
	re, _ := e.Push(mkFrame(1, 1, 1, 2, 4, true)) // 2 < wm 7
	if re {
		t.Fatal("out-of-window frame must not trigger recompute")
	}
	if e.View().WatermarkMicros != before {
		t.Fatal("water mark moved on out-of-window frame")
	}
	if len(e.OutOfWindow()) != 1 {
		t.Fatalf("out of window = %d", len(e.OutOfWindow()))
	}
}

func TestRecomputeIsAtomicAndDeterministic(t *testing.T) {
	base := []*model.Frame{
		mkFrame(1, 1, 0, 0, 0, true),
		mkFrame(1, 1, 1, 5, 4, true),
		mkFrame(1, 1, 2, 10, 8, true),
		mkFrame(1, 1, 3, 15, 12, true),
	}
	// Reverse arrival order.
	rev := make([]*model.Frame, len(base))
	for i := range base {
		rev[i] = base[len(base)-1-i]
	}
	forward := Replay(base, 100)
	reverse := Replay(rev, 100)
	if forward.Fingerprint() != reverse.Fingerprint() {
		t.Fatalf("replay mismatch:\n%s\nvs\n%s", forward.Fingerprint(), reverse.Fingerprint())
	}
	if forward.FrameCount != 4 {
		t.Fatalf("count = %d", forward.FrameCount)
	}
}

func TestSourceSwitchOpensNewBoundaryNoErrorMerge(t *testing.T) {
	e := NewEngine(1000)
	bad := mkFrame(1, 1, 0, 0, 0, false)
	good := mkFrame(1, 2, 0, 0, 8, true) // different source => new boundary
	if !SourceSwitch(bad, good) || SourceSwitch(bad, bad) {
		t.Fatal("source switch detection wrong")
	}
	e.Push(bad)
	e.Push(good)
	v := e.View()
	if v.BoundaryCount() != 2 {
		t.Fatalf("boundaries = %d want 2", v.BoundaryCount())
	}
	b0 := v.BoundaryFor(1, 1)
	b1 := v.BoundaryFor(1, 2)
	if len(b0.Errors) != 1 || len(b1.Errors) != 0 {
		t.Fatalf("errors leaked across boundaries: %d / %d", len(b0.Errors), len(b1.Errors))
	}
}

func TestEveryFrameMapsBackToOffset(t *testing.T) {
	e := NewEngine(1000)
	frames := []*model.Frame{
		mkFrame(1, 1, 0, 0, 100, true),
		mkFrame(1, 1, 1, 5, 200, true),
		mkFrame(2, 1, 0, 5, 300, true),
	}
	for _, f := range frames {
		e.Push(f)
	}
	v := e.View()
	seen := map[int64]bool{}
	for _, f := range v.Ordered {
		if f.Offset.Start < 0 {
			t.Fatal("frame without traceable offset")
		}
		seen[f.Offset.Start] = true
	}
	if len(seen) != 3 {
		t.Fatalf("traceable offsets = %d want 3", len(seen))
	}
}

func TestValidFramesExcludesInvalid(t *testing.T) {
	e := NewEngine(1000)
	e.Push(mkFrame(1, 1, 0, 0, 0, true))
	e.Push(mkFrame(1, 1, 1, 5, 4, false))
	valid := e.View().ValidFrames()
	if len(valid) != 1 || !valid[0].Valid {
		t.Fatalf("valid frames = %d", len(valid))
	}
}
