package clock

import (
	"errors"

	"testing"
)

func TestRateReduce(t *testing.T) {
	r, err := NewRate(88200, 2)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != 44100 || r.Den != 1 {
		t.Fatalf("got %+v", r)
	}
	if _, err := NewRate(0, 1); err != ErrInvalidRate {
		t.Fatalf("want ErrInvalidRate, got %v", err)
	}
	if _, err := NewRate(1, -1); err != ErrInvalidRate {
		t.Fatalf("want ErrInvalidRate, got %v", err)
	}
}

func TestRescaleRoundTrip(t *testing.T) {
	cases := []struct {
		n    int64
		from Rate
		to   Rate
		want int64
	}{
		{44100, Rate44100, Rate48000, 48000},
		{48000, Rate48000, Rate44100, 44100},
		{1000, RateMilli, Rate48000, 48000},
		{48000, Rate48000, RateMilli, 1000},
		{1234, Rate48000, Rate90000, 2314}, // 2313.75 rounds to 2314
	}
	for _, c := range cases {
		got, err := Rescale(c.n, c.from, c.to)
		if err != nil {
			t.Fatalf("%v: %v", c, err)
		}
		if got != c.want {
			t.Fatalf("Rescale(%d,%v->%v)=%d want %d", c.n, c.from, c.to, got, c.want)
		}
	}
}

func TestRescaleRejectsNegative(t *testing.T) {
	if _, err := Rescale(-1, Rate48000, Rate44100); err != ErrNegative {
		t.Fatalf("want ErrNegative, got %v", err)
	}
}

func TestRescaleHalfAwayFromZero(t *testing.T) {
	// 1 tick at 2 ticks/s to 1 tick/s is exactly 0.5 -> rounds up to 1.
	r2 := MustRate(2, 1)
	r1 := MustRate(1, 1)
	got, err := Rescale(1, r2, r1)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("got %d want 1", got)
	}
}

func TestAccumulatorBoundedErrorLongRun(t *testing.T) {
	acc, err := NewAccumulator(Rate44100, Rate48000)
	if err != nil {
		t.Fatal(err)
	}
	var sum int64
	const chunk = 1024
	const chunks = 5000
	for i := 0; i < chunks; i++ {
		n, err := acc.Convert(chunk)
		if err != nil {
			t.Fatal(err)
		}
		sum += n
		if frac := acc.ErrorTicks(); frac < 0 || frac >= 1 {
			t.Fatalf("carry error %v out of [0,1)", frac)
		}
	}
	exact := int64(chunk * chunks * 48000 / 44100)
	if sum > exact {
		t.Fatalf("streaming sum %d exceeds exact floor %d", sum, exact)
	}
	if exact-sum != 0 {
		// floor accumulation may fall short by a sub-tick carried fraction
		if exact-sum > 1 {
			t.Fatalf("drift %d ticks exceeds 1", exact-sum)
		}
	}
	carry, den := acc.ErrorTicksExact()
	if carry < 0 || carry >= den {
		t.Fatalf("carry %d not proper fraction of %d", carry, den)
	}
}

func TestAccumulatorIsExactForIntegralRatio(t *testing.T) {
	acc, err := NewAccumulator(Rate48000, Rate48000)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		if n, _ := acc.Convert(37); n != 37 {
			t.Fatalf("identity conversion returned %d", n)
		}
	}
	if carry, _ := acc.ErrorTicksExact(); carry != 0 {
		t.Fatalf("identity conversion carried %d", carry)
	}
}

func TestAccumulatorRejectsNegative(t *testing.T) {
	acc, _ := NewAccumulator(Rate44100, Rate48000)
	if _, err := acc.Convert(-5); err != ErrNegative {
		t.Fatalf("want ErrNegative, got %v", err)
	}
}

func TestMilliseconds(t *testing.T) {
	ms, err := TicksToMilliseconds(48000, Rate48000)
	if err != nil || ms != 1000 {
		t.Fatalf("got %d %v", ms, err)
	}
	ticks, err := MillisecondsToTicks(1500, Rate44100)
	if err != nil || ticks != 66150 {
		t.Fatalf("got %d %v", ticks, err)
	}
}

func TestMulCheckedOverflow(t *testing.T) {
	if _, err := mulChecked(maxInt64, 2); err != ErrOverflow {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestTimelineNonZeroStart(t *testing.T) {
	tl, _ := NewTimeline(Rate48000)
	if err := tl.SetStart(96000); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.AddMedia("audio", 4800, 48000); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.AddGap(2400); err != nil {
		t.Fatal(err)
	}
	if _, err := tl.AddMedia("audio", 55200, 1200); err != nil {
		t.Fatal(err)
	}
	if err := tl.Validate(); err != nil {
		t.Fatal(err)
	}
	if tl.Duration() != 51600 {
		t.Fatalf("duration %d", tl.Duration())
	}
	if tl.MediaDuration() != 49200 {
		t.Fatalf("media %d", tl.MediaDuration())
	}
	if tl.MissingDuration() != 2400 {
		t.Fatalf("missing %d", tl.MissingDuration())
	}
}

func TestTimelineStartQueries(t *testing.T) {
	tl, _ := NewTimeline(Rate48000)
	if err := tl.SetStart(96000); err != nil {
		t.Fatal(err)
	}
	if s, ok := tl.Start(); !ok || s != 96000 {
		t.Fatalf("start %d ok=%v", s, ok)
	}
}

func TestTimelineRejectsImplicitGap(t *testing.T) {
	tl, _ := NewTimeline(Rate48000)
	tl.SetStart(0)
	tl.AddMedia("a", 0, 100)
	_, err := tl.AddSegment(Segment{Kind: Media, Start: 150, Duration: 10})
	if err == nil {
		t.Fatal("expected cursor mismatch error")
	}
	tl.segments[0].Start = -5
	if err := tl.Validate(); !errors.Is(err, ErrOverlap) {
		t.Fatalf("want overlap error, got %v", err)
	}
	tl.segments[0].Start = 0
	tl.segments = append(tl.segments, Segment{Kind: Media, Start: 120, Duration: 10, MediaStart: 120})
	tl.cursor = 130
	if err := tl.Validate(); !errors.Is(err, ErrImplicitGap) {
		t.Fatalf("want implicit gap error, got %v", err)
	}
}

func TestTimelineRejectsBeforeStart(t *testing.T) {
	tl, _ := NewTimeline(Rate48000)
	if err := tl.SetStart(-1); err != ErrNegativeStart {
		t.Fatalf("want ErrNegativeStart, got %v", err)
	}
	if _, err := tl.AddMedia("a", 0, 10); err == nil {
		t.Fatal("expected error when start unset")
	}
}

func TestTimelineCovered(t *testing.T) {
	tl, _ := NewTimeline(Rate48000)
	tl.SetStart(0)
	tl.AddMedia("a", 0, 100)
	tl.AddGap(50)
	tl.AddMedia("a", 100, 100)
	if !tl.Covered(0, 100) {
		t.Fatal("expected first media covered")
	}
	if tl.Covered(100, 150) {
		t.Fatal("gap must not be covered")
	}
	if !tl.Covered(150, 250) {
		t.Fatal("second media must be covered")
	}
	if tl.Covered(50, 200) {
		t.Fatal("range spanning gap must not be covered")
	}
}

func TestEditMapEmptyEdit(t *testing.T) {
	em, err := NewEditMap([]EditEntry{NewEmptyEdit(4800), {MediaTime: 12000, Duration: 9600}}, Rate48000, Rate48000, 96000, 100000)
	if err != nil {
		t.Fatal(err)
	}
	pres, ok := em.Map(12000)
	if !ok || pres != 96000+4800 {
		t.Fatalf("map(12000)=%d ok=%v", pres, ok)
	}
	if _, ok := em.PresentationToMedia(96000); ok {
		t.Fatal("empty edit must not map to media")
	}
	media, ok := em.PresentationToMedia(96000 + 4800 + 100)
	if !ok || media != 12100 {
		t.Fatalf("toMedia=%d ok=%v", media, ok)
	}
	if !em.CoversPresentation(96100) {
		t.Fatal("empty edit should still be covered in presentation")
	}
	if em.PresentationEnd() != 96000+4800+9600 {
		t.Fatalf("end %d", em.PresentationEnd())
	}
}

func TestEditMapOutOfBounds(t *testing.T) {
	_, err := NewEditMap([]EditEntry{{MediaTime: 9000, Duration: 9600}}, Rate48000, Rate48000, 0, 10000)
	if !errors.Is(err, ErrEditOutOfBounds) {
		t.Fatalf("expected ErrEditOutOfBounds, got %v", err)
	}
}

func TestEditMapRejectsMalformed(t *testing.T) {
	if _, err := NewEditMap(nil, Rate48000, Rate48000, 0, 1); err == nil {
		t.Fatal("empty list must fail")
	}
	if _, err := NewEditMap([]EditEntry{{MediaTime: 0, Duration: 0}}, Rate48000, Rate48000, 0, 1); err == nil {
		t.Fatal("zero duration must fail")
	}
}

func TestUnion(t *testing.T) {
	in := []Interval{{0, 10}, {5, 10}, {30, 5}, {35, 5}, {10, 1}}
	out := Union(in)
	if len(out) != 2 {
		t.Fatalf("got %v", out)
	}
	if out[0].Start != 0 || out[0].Duration != 15 {
		t.Fatalf("first %+v", out[0])
	}
	if out[1].Start != 30 || out[1].Duration != 10 {
		t.Fatalf("second %+v", out[1])
	}
}

func TestUnionMergesAdjacent(t *testing.T) {
	out := Union([]Interval{{0, 10}, {10, 5}, {20, 5}})
	if len(out) != 2 {
		t.Fatalf("got %v", out)
	}
	if out[0].Duration != 15 || out[1].Duration != 5 || out[1].Start != 20 {
		t.Fatalf("got %v", out)
	}
}

func TestSortedIntervalsRejectsOverlap(t *testing.T) {
	if _, err := SortedIntervals([]Interval{{0, 10}, {5, 5}}); err != ErrOverlap {
		t.Fatalf("want ErrOverlap, got %v", err)
	}
}
