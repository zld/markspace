package model

import (
	"errors"
	"math"
	"testing"
)

func TestFramePTSNS_ExactRates(t *testing.T) {
	cases := []struct {
		fps   Rational
		frame int64
		want  int64
	}{
		{Rational{30, 1}, 0, 0},
		{Rational{30, 1}, 1, 33_333_333},
		{Rational{30, 1}, 30, 1_000_000_000},
		{Rational{25, 1}, 1, 40_000_000},
		{Rational{24, 1}, 24, 1_000_000_000},
	}
	for _, tc := range cases {
		got, err := FramePTSNS(tc.frame, tc.fps)
		if err != nil {
			t.Fatalf("FramePTSNS(%d): %v", tc.frame, err)
		}
		if got != tc.want {
			t.Errorf("FramePTSNS frame=%d fps=%d/%d: got %d want %d", tc.frame, tc.fps.Num, tc.fps.Den, got, tc.want)
		}
	}
}

func TestFramePTSNS_NoAccumulatedDrift(t *testing.T) {
	// NTSC rate cannot be represented exactly; per-frame rounding must stay
	// bounded instead of drifting across a long sequence.
	fps := Rational{30000, 1001}
	var maxErr int64
	for f := int64(1); f <= 300_000; f++ {
		got, err := FramePTSNS(f, fps)
		if err != nil {
			t.Fatalf("frame %d: %v", f, err)
		}
		// Exact value as high precision float, only used to bound the integer
		// rounding result (not to produce pipeline timestamps).
		exact := float64(f) * 1001.0 / 30000.0 * float64(NanoPerSec)
		diff := int64(math.Abs(float64(got) - exact))
		if diff > maxErr {
			maxErr = diff
		}
	}
	if maxErr > 1 {
		t.Fatalf("per-frame rounding error reached %d ns, want <= 1 ns", maxErr)
	}
}

func TestTicksRoundTripWithinOneNS(t *testing.T) {
	fps := Rational{30000, 1001}
	// Timescales chosen by normalization must be multiples of the frame-rate
	// numerator, so every frame boundary lands on an exact tick.
	for _, ts := range []uint32{30_000, 90_000, 300_000, 3_000_000} {
		var worst int64
		for f := int64(0); f < 10_000; f++ {
			errNS, err := AlignmentErrorNS(f, ts, fps)
			if err != nil {
				t.Fatalf("ts=%d frame=%d: %v", ts, f, err)
			}
			if errNS > worst {
				worst = errNS
			}
		}
		if worst > 1 {
			t.Errorf("aligned timescale %d alignment error %d ns exceeds 1 ns", ts, worst)
		}
	}
}

func TestTicksRoundTripBoundedForArbitraryTimescale(t *testing.T) {
	// With a non-aligned timescale the error is bounded by one tick and never
	// accumulates across frames.
	fps := Rational{30000, 1001}
	ts := uint32(48_000)
	tickNS := int64(NanoPerSec) / int64(ts)
	var worst int64
	for f := int64(0); f < 50_000; f++ {
		errNS, err := AlignmentErrorNS(f, ts, fps)
		if err != nil {
			t.Fatalf("frame=%d: %v", f, err)
		}
		if errNS > worst {
			worst = errNS
		}
	}
	if worst > tickNS {
		t.Errorf("unbounded drift: worst %d ns exceeds one tick of %d ns", worst, tickNS)
	}
}

func TestZeroTimebaseRejected(t *testing.T) {
	if _, err := TicksToNS(10, 0); err == nil {
		t.Fatal("expected error for zero timescale")
	} else if se, ok := AsStructured(err); !ok || se.Code != ErrZeroTimebase {
		t.Fatalf("want ErrZeroTimebase, got %v", err)
	}
	if _, err := FramePTSTicks(1, 0, Rational{30, 1}); err == nil {
		t.Fatal("expected error for zero timescale in FramePTSTicks")
	}
	if err := (Rational{0, 1}).Validate(false); err == nil {
		t.Fatal("expected zero-rate rational to be rejected")
	}
}

func TestOverflowChecked(t *testing.T) {
	if _, err := CheckedMul(math.MaxInt64, 2); err == nil {
		t.Fatal("expected overflow error from CheckedMul")
	}
	if _, err := CheckedAdd(math.MaxInt64, 1); err == nil {
		t.Fatal("expected overflow error from CheckedAdd")
	}
}

func TestRationalCmp(t *testing.T) {
	a := Rational{30000, 1001}
	b := Rational{30, 1}
	if a.Cmp(b) != -1 || b.Cmp(a) != 1 || a.Cmp(a) != 0 {
		t.Fatal("Rational.Cmp mismatch")
	}
}

func TestStructuredError(t *testing.T) {
	base := New(ErrIllegalHeader, "av1", 42, "bad obu")
	inner := errors.New("io: nope")
	wrapped := base.Wrap(inner)
	if !errors.Is(wrapped, inner) {
		t.Fatal("errors.Is should find wrapped cause")
	}
	var se *Error
	if !errors.As(wrapped, &se) || se.ByteOffset != 42 || se.Codec != "av1" {
		t.Fatalf("errors.As failed: %+v", wrapped)
	}
	if ExitCodeFor(base) != ExitCorrupt {
		t.Fatal("corrupt code must map to exit 2")
	}
	if ExitCodeFor(errors.New("boom")) != ExitGeneric {
		t.Fatal("plain error must map to exit 1")
	}
	if ExitCodeFor(nil) != ExitOK {
		t.Fatal("nil error must map to exit 0")
	}
}
