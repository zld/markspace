package timebase

import "testing"

func TestUnwrapMonotonicAndWrap(t *testing.T) {
	var c Clock33
	seq := []uint64{100, 200, Mask33 - 50, 10, 20}
	wantAbs := []uint64{100, 200, Mask33 - 50, Mask33 + 10, Mask33 + 20}
	for i, raw := range seq {
		if got := c.Unwrap(raw); got != wantAbs[i] {
			t.Fatalf("step %d: got abs %d want %d", i, got, wantAbs[i])
		}
	}
	if c.Wraps() != 1 {
		t.Fatalf("wraps=%d want 1", c.Wraps())
	}
}

func TestUnwrapRepeatedValueHolds(t *testing.T) {
	var c Clock33
	c.Unwrap(5000)
	a := c.Unwrap(5000)
	b := c.Unwrap(5000)
	if a != 5000 || b != 5000 || c.Wraps() != 0 {
		t.Fatalf("repeated samples must hold: %d %d wraps=%d", a, b, c.Wraps())
	}
}

func TestUnwrapMasks33BitInput(t *testing.T) {
	var c Clock33
	got := c.Unwrap(Mask33 | 123)
	if got != 123 {
		t.Fatalf("raw input not masked: %d", got)
	}
}

func TestRegressionDetection(t *testing.T) {
	if Regression(100, 90) != true {
		t.Fatal("small backward step must be a regression")
	}
	if Regression(10, Mask33-20) != false {
		t.Fatal("forward step is not a regression")
	}
	if Regression(100, 100) {
		t.Fatal("equal values are not a regression")
	}
	// Small forward distance across the modulus boundary is a legitimate
	// wrap, not a regression: prev=2^33-2, raw=3, distance=5.
	if Regression(Mask33-2, 3) {
		t.Fatal("wrap-sized jump must not be a regression")
	}
}

func TestPCRTo27MHz(t *testing.T) {
	// base=1, ext=0 with one wrap: (2^33+1)*300
	got := PCRTo27MHz(1, 0, 1)
	want := (Mask33 + 1) * 300
	if got != want {
		t.Fatalf("pcr expansion got %d want %d", got, want)
	}
	if got := PCRTo27MHz(0, 300&0x1FF, 0); got != 300 {
		t.Fatalf("extension not encoded: %d", got)
	}
}

func TestSecondsConversions(t *testing.T) {
	if Seconds90k(90000) != 1.0 {
		t.Fatal("90k ticks should be one second")
	}
	if Seconds27MHz(27000000) != 1.0 {
		t.Fatal("27M ticks should be one second")
	}
}

func TestDeterministicRerun(t *testing.T) {
	raws := []uint64{7, 1000, Mask33 - 1, 5, 99, 1000}
	run := func() []uint64 {
		var c Clock33
		out := make([]uint64, len(raws))
		for i, r := range raws {
			out[i] = c.Unwrap(r)
		}
		return out
	}
	first, second := run(), run()
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("nondeterministic at %d: %d vs %d", i, first[i], second[i])
		}
	}
}
