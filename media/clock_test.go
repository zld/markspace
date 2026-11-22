package media

import (
	"testing"
)

func TestUnwrapDelta(t *testing.T) {
	cases := []struct {
		name     string
		prev     uint32
		cur      uint32
		wantSign int
		wantAbs  int64
	}{
		{"forward", 100, 150, 1, 50},
		{"backward simple", 150, 100, -1, 50},
		{"wrap forward", 0xFFFFFF00, 0x00000100, 1, 512},
		{"wrap exact", 0xFFFFFFFF, 0, 1, 1},
		{"wrap backward", 0x00000100, 0xFFFFFF00, -1, 512},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := UnwrapDelta(tc.prev, tc.cur)
			if (d > 0) != (tc.wantSign > 0) || d == 0 {
				t.Fatalf("UnwrapDelta(%d,%d)=%d, want sign %d", tc.prev, tc.cur, d, tc.wantSign)
			}
			if abs := d; abs < 0 && -abs != tc.wantAbs || abs > 0 && abs != tc.wantAbs {
				t.Fatalf("UnwrapDelta(%d,%d)=%d, want magnitude %d", tc.prev, tc.cur, d, tc.wantAbs)
			}
		})
	}
}

func TestClockMonotonicAcrossWrap(t *testing.T) {
	c := MustClock(DefaultTimescale)
	step := c.FromMS(40)
	start := uint32(0xFFFFF000)
	var lastTicks int64
	sawWrap := false
	for i := 0; i < 100; i++ {
		raw := start + uint32(int64(i)*step)
		p := c.Observe(raw)
		if p.Regressed {
			t.Fatalf("frame %d reported regression at ticks=%d", i, p.Ticks)
		}
		if p.Wrapped {
			sawWrap = true
		}
		if i > 0 && p.Ticks-lastTicks != step {
			t.Fatalf("frame %d delta=%d want %d", i, p.Ticks-lastTicks, step)
		}
		lastTicks = p.Ticks
	}
	if !sawWrap {
		t.Fatal("expected a 32-bit wrap event")
	}
	if want := c.FromMS(40 * 99); lastTicks != want {
		t.Fatalf("final ticks=%d want %d", lastTicks, want)
	}
}

func TestClockRegressionDetection(t *testing.T) {
	c := MustClock(1000)
	c.SetToleranceMS(10)
	c.Observe(10_000)
	c.Observe(10_040)
	// 5 ms backwards is inside tolerance (jitter).
	if p := c.Observe(10_035); p.Regressed {
		t.Fatalf("5ms backwards should stay within tolerance: %+v", p)
	}
	// 50 ms backwards is a real regression.
	if p := c.Observe(9_985); !p.Regressed {
		t.Fatalf("50ms backwards should be a regression: %+v", p)
	}
	// Recovery forward continues on the monotonic timeline. Ticks are
	// relative to the first observation (10_000).
	p := c.Observe(10_080)
	if p.Regressed || p.Ticks != 80 {
		t.Fatalf("recovery = %+v, want ticks 80", p)
	}
}

func TestClockConvertRoundTrip(t *testing.T) {
	for _, ts := range []uint32{90000, 1000, 48000, 44100} {
		c := MustClock(ts)
		for _, ms := range []int64{0, 1, 40, 1200, 60000} {
			if got := c.ToMS(c.FromMS(ms)); got != ms {
				t.Fatalf("timescale %d ms %d round-tripped to %d", ts, ms, got)
			}
		}
	}
}

func TestClockBoundaryComparisons(t *testing.T) {
	c := MustClock(90000)
	a := c.FromMS(1000)
	b := a + c.FromMS(2)
	if !c.WithinMS(a, b, 2) {
		t.Fatalf("2ms apart should match a 2ms tolerance")
	}
	if c.WithinMS(a, b+c.FromMS(1), 2) {
		t.Fatalf("3ms apart should not match a 2ms tolerance")
	}
	if CompareTicks(a, b, 0) != -1 {
		t.Fatalf("strict compare should order a before b")
	}
	if CompareTicks(a, b, c.FromMS(2)) != 0 {
		t.Fatalf("tolerant compare should treat 2ms apart as equal")
	}
}

func TestNewClockRejectsZeroTimescale(t *testing.T) {
	if _, err := NewClock(0); err == nil {
		t.Fatal("expected error for zero timescale")
	} else if CategoryOf(err) != CatInvalidParams {
		t.Fatalf("category=%s want %s", CategoryOf(err), CatInvalidParams)
	}
}
