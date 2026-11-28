package clock

import (
	"errors"
	"math"
	"testing"
)

func TestToNanosRoundTrip(t *testing.T) {
	tb := Timebase{Num: 1, Den: 90000}
	ns, err := tb.ToNanos(90000)
	if err != nil || ns != 1_000_000_000 {
		t.Fatalf("ToNanos(90000) = %d, %v", ns, err)
	}
	back, err := tb.FromNanos(ns)
	if err != nil || back != 90000 {
		t.Fatalf("FromNanos = %d, %v", back, err)
	}
	// VFR-style odd duration: 1501 ticks @ 1/90000
	ns, err = tb.ToNanos(1501)
	if err != nil || ns != 16_677_777 {
		t.Fatalf("ToNanos(1501) = %d, %v", ns, err)
	}
}

func TestToNanosOverflow(t *testing.T) {
	tb := Timebase{Num: 1, Den: 1}
	if _, err := tb.ToNanos(math.MaxInt64); !errors.Is(err, ErrOverflow) {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
	if _, err := (Timebase{Num: 0, Den: 1}).ToNanos(1); err == nil {
		t.Fatal("invalid timebase accepted")
	}
}

func TestSeqUnwrapAcrossWrap(t *testing.T) {
	var u SeqUnwrapper
	for _, s := range []uint16{65534, 65535, 0, 1} {
		if _, err := u.Unwrap(s); err != nil {
			t.Fatal(err)
		}
	}
	// Late packet from previous cycle arrives after wrap.
	got, err := u.Unwrap(65535)
	if err != nil {
		t.Fatal(err)
	}
	if got != 65535 {
		t.Fatalf("late seq 65535 mapped to %d, want 65535", got)
	}
	got, err = u.Unwrap(2)
	if err != nil {
		t.Fatal(err)
	}
	if got != 65538 {
		t.Fatalf("seq 2 mapped to %d, want 65538", got)
	}
}

func TestSeqUnwrapAmbiguous(t *testing.T) {
	var u SeqUnwrapper
	if _, err := u.Unwrap(1000); err != nil {
		t.Fatal(err)
	}
	if _, err := u.Unwrap(1000 + 0x8000); !errors.Is(err, ErrAmbiguousOrder) {
		t.Fatalf("want ErrAmbiguousOrder, got %v", err)
	}
}

func TestTSUnwrapAcrossWrap(t *testing.T) {
	var u TSUnwrapper
	if _, err := u.Unwrap(0xFFFFFFF0); err != nil {
		t.Fatal(err)
	}
	got, err := u.Unwrap(0x00000010)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1<<32+0x10 {
		t.Fatalf("wrapped ts mapped to %d", got)
	}
}

func TestDriftEstimator(t *testing.T) {
	var d DriftEstimator
	// Remote clock runs 100 PPM fast with a 5ms constant offset.
	for i := int64(0); i <= 100; i++ {
		local := i * 20_000_000 // 20ms steps
		remote := local + local/10_000 + 5_000_000
		d.Add(local, remote)
	}
	off, ppm, ok := d.Correction()
	if !ok {
		t.Fatal("no correction")
	}
	if off != 5_000_000 {
		t.Fatalf("offset = %d, want 5ms", off)
	}
	if math.Abs(ppm-100) > 1 {
		t.Fatalf("slope = %f ppm, want ~100", ppm)
	}
	// Apply removes the correction: corrected remote ~= local (2nd-order
	// residual of slope*offset is tolerated).
	got := d.Apply(1_000_000_000 + 1_000_000_000/10_000 + 5_000_000)
	if math.Abs(float64(got-1_000_000_000)) > 50 {
		t.Fatalf("Apply = %d, want ~1s", got)
	}
}
