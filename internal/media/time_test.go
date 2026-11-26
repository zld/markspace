package media

import (
	"math"
	"testing"
)

func TestNewRationalNormalises(t *testing.T) {
	cases := []struct {
		num, den, wantNum, wantDen int64
	}{
		{2, 4, 1, 2},
		{-6, 8, -3, 4},
		{6, -8, -3, 4},
		{10, 5, 2, 1},
		{0, 7, 0, 1},
		{3, 0, 3, 1},
	}
	for _, c := range cases {
		got := NewRational(c.num, c.den)
		if got.Num != c.wantNum || got.Den != c.wantDen {
			t.Errorf("NewRational(%d,%d)=%v want %d/%d", c.num, c.den, got, c.wantNum, c.wantDen)
		}
	}
}

func TestArithmetic(t *testing.T) {
	a := FromMicros(500_000) // 1/2
	b := NewRational(1, 3)
	if got := a.Add(b); got.Cmp(NewRational(5, 6)) != 0 {
		t.Fatalf("add: %v", got)
	}
	if got := a.Sub(b); got.Cmp(NewRational(1, 6)) != 0 {
		t.Fatalf("sub: %v", got)
	}
	if got := b.MulInt(3); got.Cmp(NewRational(1, 1)) != 0 {
		t.Fatalf("mul: %v", got)
	}
	if !Zero.Less(a) || !a.Less(NewRational(3, 5)) || a.Greater(NewRational(1, 2)) {
		t.Fatal("comparisons")
	}
	if a.Neg().Num != -1 || a.Neg().Den != 2 {
		t.Fatalf("neg: %v", a.Neg())
	}
}

func TestTicksRoundTripAtCommonTimescale(t *testing.T) {
	// 30000/1001 fps frame duration must survive to ticks and back when the
	// timescale is chosen as the LCM.
	ts := CommonTimescale(30000, 90000)
	if ts != 90000 {
		t.Fatalf("lcm=%d", ts)
	}
	frame := FromTicks(1001, 30000)
	ticks := frame.Scale(ts)
	if ticks != 3003 {
		t.Fatalf("ticks=%d", ticks)
	}
	if back := FromTicks(ticks, ts); back.Cmp(frame) != 0 {
		t.Fatalf("round trip %v != %v", back, frame)
	}
}

func TestMicrosRounding(t *testing.T) {
	// 1 tick at 90000 Hz is 11.111... us.
	d := FromTicks(1, 90000)
	if got := d.Micros(); got != 11 {
		t.Fatalf("micros=%d", got)
	}
	if got := d.MicrosCeil(); got != 12 {
		t.Fatalf("micros ceil=%d", got)
	}
	neg := FromTicks(-1, 90000)
	if got := neg.Micros(); got != -11 {
		t.Fatalf("negative micros=%d", got)
	}
	if got := neg.MicrosCeil(); got != 12 {
		t.Fatalf("negative ceil=%d", got)
	}
	if got := FromMicros(0).Micros(); got != 0 {
		t.Fatalf("zero=%d", got)
	}
}

func TestClamp(t *testing.T) {
	lo, hi := FromMillis(10), FromMillis(20)
	if Clamp(FromMillis(5), lo, hi).Cmp(lo) != 0 {
		t.Fatal("low clamp")
	}
	if Clamp(FromMillis(25), lo, hi).Cmp(hi) != 0 {
		t.Fatal("high clamp")
	}
	if Clamp(FromMillis(15), lo, hi).Cmp(FromMillis(15)) != 0 {
		t.Fatal("inside clamp")
	}
}

func TestCommonTimescaleFallback(t *testing.T) {
	if got := CommonTimescale(0, 1000); got != 1000 {
		t.Fatalf("zero a: %d", got)
	}
	if got := CommonTimescale(1000, 0); got != 1000 {
		t.Fatalf("zero b: %d", got)
	}
	a, b := uint32(math.MaxUint32), uint32(2)
	if got := CommonTimescale(a, b); got != a {
		t.Fatalf("overflow fallback=%d", got)
	}
}
