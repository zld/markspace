package timeline

import (
	"math"
	"testing"

	mediaerr "mediacheck/internal/errors"
)

func TestRescaleKnownRatios(t *testing.T) {
	src := TimeBase{Num: 1, Den: 30} // 1/30 s per tick (30 fps ticks)
	dst := TimeBase{Num: 1, Den: 90000}
	cases := []struct {
		in, want int64
	}{
		{0, 0},
		{1, 3000},
		{30, 90000},
		{2, 6000},
	}
	for _, c := range cases {
		got, err := Rescale(c.in, src, dst)
		if err != nil {
			t.Fatalf("Rescale(%d): %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("Rescale(%d)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestRescaleRoundTripIdentity(t *testing.T) {
	a := TimeBase{Num: 1, Den: 25}
	b := TimeBase{Num: 1, Den: 90000}
	for _, ticks := range []int64{1, 7, 100, 123456} {
		mid, err := Rescale(ticks, a, b)
		if err != nil {
			t.Fatal(err)
		}
		back, err := Rescale(mid, b, a)
		if err != nil {
			t.Fatal(err)
		}
		if back != ticks {
			t.Fatalf("round trip %d -> %d -> %d", ticks, mid, back)
		}
	}
}

func TestRescaleOverflow(t *testing.T) {
	src := TimeBase{Num: 1, Den: 1}
	dst := TimeBase{Num: 1, Den: math.MaxInt64}
	if _, err := Rescale(math.MaxInt64, src, dst); err == nil {
		t.Fatal("expected overflow error")
	} else if me, ok := mediaerr.AsMedia(err); !ok || me.Stage != mediaerr.StageTimebase {
		t.Fatalf("want timebase media error, got %v", err)
	}
}

func TestInvalidTimeBase(t *testing.T) {
	if (TimeBase{Num: 0, Den: 1}).Check("v") == nil {
		t.Fatal("zero numerator must be invalid")
	}
	if (TimeBase{Num: 1, Den: 0}).Check("v") == nil {
		t.Fatal("zero denominator must be invalid")
	}
}

func TestStampAddOverflow(t *testing.T) {
	s := NewStamp(math.MaxInt64, TimeBase{Num: 1, Den: 1})
	if _, err := s.Add(1); err == nil {
		t.Fatal("expected overflow")
	}
	s2, err := NewStamp(10, TimeBase{Num: 1, Den: 1}).Add(5)
	if err != nil || s2.TS != 15 || s2.OriginalTS != 10 {
		t.Fatalf("add failed: %+v err=%v", s2, err)
	}
}
