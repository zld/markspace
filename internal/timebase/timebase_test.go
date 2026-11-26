package timebase

import "testing"

func TestUnwrapModulus33(t *testing.T) {
	u := NewUnwrapper(1 << 33)
	base := int64(1<<33) - 100
	vals := []int64{base, base + 50, 10, 60} // wraps across 2^33
	want := []int64{base, base + 50, 1<<33 + 10, 1<<33 + 60}
	for i, v := range vals {
		got, err := u.Unwrap(v)
		if err != nil {
			t.Fatalf("Unwrap(%d) error: %v", v, err)
		}
		if got != want[i] {
			t.Fatalf("Unwrap(%d) = %d, want %d", v, got, want[i])
		}
	}
}

func TestUnwrapRTP32(t *testing.T) {
	u := NewUnwrapper(1 << 32)
	seq := []int64{(1 << 32) - 5, (1 << 32) - 1, 0, 7}
	want := []int64{(1 << 32) - 5, (1 << 32) - 1, 1 << 32, 1<<32 + 7}
	for i, v := range seq {
		got, err := u.Unwrap(v)
		if err != nil || got != want[i] {
			t.Fatalf("step %d: got %d err %v, want %d", i, got, err, want[i])
		}
	}
}

func TestUnwrapAmbiguous(t *testing.T) {
	u := NewUnwrapper(1 << 32)
	if _, err := u.Unwrap(0); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Unwrap(1 << 31); err != ErrAmbiguousJump {
		t.Fatalf("want ErrAmbiguousJump, got %v", err)
	}
}

func TestUnwrapSeed(t *testing.T) {
	u := NewUnwrapper(1 << 33)
	u.Seed(1<<33 + 500)       // epoch hint near true clock
	got, err := u.Unwrap(500) // raw PTS just after wrap
	if err != nil {
		t.Fatal(err)
	}
	if got != 1<<33+500 {
		t.Fatalf("got %d, want %d", got, 1<<33+500)
	}
}

func TestClockDriftAndOffset(t *testing.T) {
	var c Clock
	// ref = 7000 + source*(1+80ppm), exact at multiples of 12500.
	for x := int64(0); x <= 100000; x += 12500 {
		c.AddAnchor(Anchor{Source: x, Ref: 7000 + x + x*80/1000000})
	}
	ppm, ok := c.DriftPPM()
	if !ok || ppm != 80 {
		t.Fatalf("DriftPPM = %d, %v; want 80, true", ppm, ok)
	}
	ref, ok := c.ToRef(50000)
	if !ok {
		t.Fatal("ToRef not ok")
	}
	want := int64(7000 + 50000 + 4)
	if ref != want {
		t.Fatalf("ToRef = %d, want %d", ref, want)
	}
}

func TestClockNoAnchors(t *testing.T) {
	var c Clock
	if _, ok := c.DriftPPM(); ok {
		t.Fatal("expected ok=false without anchors")
	}
	if _, ok := c.ToRef(1); ok {
		t.Fatal("expected ok=false without anchors")
	}
}

func TestClockDeterministic(t *testing.T) {
	build := func() *Clock {
		c := &Clock{}
		for x := int64(12500); x <= 87500; x += 12500 {
			c.AddAnchor(Anchor{Source: x, Ref: 123 + x + x*80/1000000})
		}
		return c
	}
	a, _ := build().DriftPPM()
	b, _ := build().DriftPPM()
	if a != b {
		t.Fatalf("non-deterministic drift: %d vs %d", a, b)
	}
}
