package segment

import (
	"errors"
	"math"
	"testing"

	"example.com/continuity/internal/clock"
	"example.com/continuity/internal/fault"
	"example.com/continuity/internal/frame"
)

func rec(seq uint64, ns int64) *frame.Record {
	return &frame.Record{
		Version: frame.Version1, Track: "video/0", Seq: seq,
		CaptureNS: ns, Width: 2, Height: 2, Payload: []byte{1, 2, 3, 4},
		Offset: int64(seq) * 100,
	}
}

func collect(t *testing.T, c *Cutter, recs []*frame.Record) []*Segment {
	t.Helper()
	var out []*Segment
	for _, r := range recs {
		done, err := c.Push(r)
		if err != nil {
			t.Fatalf("seq=%d: %v", r.Seq, err)
		}
		if done != nil {
			out = append(out, done)
		}
	}
	last, err := c.Finish()
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if last != nil {
		out = append(out, last)
	}
	return out
}

func TestSingleSegment(t *testing.T) {
	c, err := New(Config{ExpectedIntervalNS: 40_000_000, JumpFactor: 2, MaxSegmentNS: 10_000_000_000})
	if err != nil {
		t.Fatal(err)
	}
	recs := []*frame.Record{rec(0, 0), rec(1, 40_000_000), rec(2, 80_000_000)}
	segs := collect(t, c, recs)
	if len(segs) != 1 {
		t.Fatalf("segments=%d", len(segs))
	}
	s := segs[0]
	if s.FrameStart != 0 || s.FrameEnd != 3 || s.StartNS != 0 || s.EndNS != 120_000_000 {
		t.Fatalf("bad segment: %+v", s)
	}
	if s.PayloadBytes != 12 {
		t.Fatalf("payload bytes=%d", s.PayloadBytes)
	}
}

func TestJumpCut(t *testing.T) {
	c, _ := New(Config{ExpectedIntervalNS: 40_000_000, JumpFactor: 2, MaxSegmentNS: math.MaxInt64})
	// Gap 200ms > threshold 80ms between seq 2 and seq 3.
	recs := []*frame.Record{
		rec(0, 0), rec(1, 40_000_000), rec(2, 80_000_000),
		rec(3, 280_000_000), rec(4, 320_000_000),
	}
	segs := collect(t, c, recs)
	if len(segs) != 2 {
		t.Fatalf("segments=%d", len(segs))
	}
	// Half-open, boundary frame in one segment only; internal end equals
	// the last timestamp inside the closed segment.
	if segs[0].FrameStart != 0 || segs[0].FrameEnd != 3 || segs[0].EndNS != 80_000_000 {
		t.Fatalf("seg0 %+v", segs[0])
	}
	if segs[1].FrameStart != 3 || segs[1].FrameEnd != 5 ||
		segs[1].StartNS != 280_000_000 || segs[1].EndNS != 360_000_000 {
		t.Fatalf("seg1 %+v", segs[1])
	}
	if segs[0].Frames[2].Seq == segs[1].Frames[0].Seq {
		t.Fatal("boundary frame duplicated across segments")
	}
}

func TestMaxDurationCut(t *testing.T) {
	c, _ := New(Config{ExpectedIntervalNS: 40_000_000, MaxSegmentNS: 100_000_000})
	recs := []*frame.Record{
		rec(0, 0), rec(1, 40_000_000), rec(2, 80_000_000), rec(3, 120_000_000),
	}
	segs := collect(t, c, recs)
	if len(segs) != 2 {
		t.Fatalf("segments=%d", len(segs))
	}
	if segs[0].EndNS != 80_000_000 || segs[1].FrameStart != 3 {
		t.Fatalf("boundary wrong: %+v %+v", segs[0], segs[1])
	}
}

func TestBackwardTimestampRejected(t *testing.T) {
	c, _ := New(Config{ExpectedIntervalNS: 40_000_000, MaxSegmentNS: 1_000_000_000})
	if _, err := c.Push(rec(0, 100)); err != nil {
		t.Fatal(err)
	}
	_, err := c.Push(rec(1, 99))
	var fe *fault.Error
	if !errors.As(err, &fe) || fe.Stage != fault.StageSegment || fe.Frame != 1 {
		t.Fatalf("got %v", err)
	}
}

func TestOverflowRejected(t *testing.T) {
	// A first frame safely below the ceiling passes the arithmetic guard.
	c, _ := New(Config{ExpectedIntervalNS: 40_000_000, MaxSegmentNS: math.MaxInt64})
	if _, err := c.Push(rec(0, math.MaxInt64-1_000_000_000)); err != nil {
		t.Fatal(err)
	}
	// A timestamp at the int64 ceiling must be refused at Push time, before
	// any checkpoint could be committed for the overflowing frame.
	_, err := c.Push(rec(1, math.MaxInt64))
	if !errors.Is(err, clock.ErrOverflow) {
		t.Fatalf("push overflow: %v", err)
	}
	// Finish on a single near-ceiling frame also overflows.
	c2, _ := New(Config{ExpectedIntervalNS: 40_000_000, MaxSegmentNS: math.MaxInt64})
	if _, err := c2.Push(rec(0, math.MaxInt64)); !errors.Is(err, clock.ErrOverflow) {
		t.Fatalf("first-frame overflow not rejected: %v", err)
	}
}

func TestBadConfig(t *testing.T) {
	if _, err := New(Config{MaxSegmentNS: 10}); err == nil {
		t.Fatal("zero interval accepted")
	}
	if _, err := New(Config{ExpectedIntervalNS: 10}); err == nil {
		t.Fatal("zero max duration accepted")
	}
}
