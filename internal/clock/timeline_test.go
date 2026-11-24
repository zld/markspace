package clock

import (
	"testing"

	"mediacheck/internal/fault"
)

func TestToNanosExact(t *testing.T) {
	ts := Timescale(90000)
	if got := ts.ToNanos(90000); got != 1_000_000_000 {
		t.Fatalf("1s in 90kHz = %d ns, want 1e9", got)
	}
	// 1 tick at 90000 = 11111.111... ns -> truncated toward zero.
	if got := ts.ToNanos(1); got != 11111 {
		t.Fatalf("1 tick = %d ns, want 11111", got)
	}
	// Round trip must be reproducible.
	for _, ticks := range []int64{0, 1, 3600, 1 << 40, -3600} {
		a := ts.ToNanos(ticks)
		b := ts.ToNanos(ticks)
		if a != b {
			t.Fatalf("ToNanos(%d) not reproducible: %d vs %d", ticks, a, b)
		}
	}
}

func TestMulDivOverflow(t *testing.T) {
	// 128-bit path: (1<<62) * (1<<30) / (1<<20) = 1<<72 -> saturates via big fallback.
	got := MulDiv(1<<62, 1<<30, 1<<20)
	if got != 1<<63-1 {
		t.Fatalf("saturating MulDiv = %d, want MaxInt64", got)
	}
	if got := MulDiv(-(1 << 62), 1<<30, 1<<20); got != -1<<63 {
		t.Fatalf("negative saturating MulDiv = %d, want MinInt64", got)
	}
	if got := MulDiv(1<<40, 1_000_000_000, 90000); got <= 0 {
		t.Fatalf("large exact MulDiv failed: %d", got)
	}
}

func TestConvertBetweenTimescales(t *testing.T) {
	video := Timescale(90000)
	audio := Timescale(48000)
	// 40ms video frame = 3600 ticks @90k = 1920 ticks @48k.
	if got := video.Convert(3600, audio); got != 1920 {
		t.Fatalf("convert 3600@90k to 48k = %d, want 1920", got)
	}
}

func TestTrackerFaults(t *testing.T) {
	fl := fault.NewList()
	tr := NewTracker("video", fl)
	tr.Check(-100, 0)   // negative start
	tr.Check(0, 8)      // ok
	tr.Check(3600, 16)  // ok
	tr.Check(1000, 24)  // non-monotonic
	tr.Check(7200, 32)  // ok again
	if !fl.Has(fault.ErrNegativeStart) {
		t.Fatal("expected NEGATIVE_START fault")
	}
	if !fl.Has(fault.ErrNonMonotonic) {
		t.Fatal("expected NON_MONOTONIC_TS fault")
	}
	if fl.Count(fault.ErrNonMonotonic) != 1 {
		t.Fatalf("want exactly 1 non-monotonic fault, got %d", fl.Count(fault.ErrNonMonotonic))
	}
	// Offsets must be recorded for cross-checking.
	for _, f := range fl.All() {
		if f.Offset < 0 {
			t.Fatalf("fault %s missing offset", f.Code)
		}
	}
}

func TestWrapTracker(t *testing.T) {
	fl := fault.NewList()
	w := NewWrapTracker(fl)
	w.Check(0xfffffff0, 0)
	w.Check(0x00000010, 4) // normal small rollover, no fault
	if fl.Has(fault.ErrWraparound) {
		t.Fatal("small rollover must not be flagged")
	}
	w.Check(0x7fffff00, 8)
	w.Check(0x00000010, 12) // huge backwards jump -> wrap/jump fault
	if !fl.Has(fault.ErrWraparound) {
		t.Fatal("expected TS_WRAPAROUND fault")
	}
}

func TestRationalNormalize(t *testing.T) {
	r := NewRational(2_000_000_000, 180000)
	if r.Num != 100000 || r.Den != 9 {
		t.Fatalf("normalized = %d/%d, want 100000/9", r.Num, r.Den)
	}
}
