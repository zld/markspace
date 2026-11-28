package clock

import (
	"math"
	"testing"

	"rtptrace/internal/fault"
)

func TestScaleBasic(t *testing.T) {
	// 10 units at 1/48000 s -> 10000 us scale (48k -> 1e6 us).
	got, err := Scale(10, Rational{Num: 1, Den: 48000}, Rational{Num: 1_000_000, Den: 1}, "dts")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 208 { // floor(10 * 1e6 / 48000)
		t.Fatalf("got %d, want 208", got)
	}
}

func TestScaleTruncates(t *testing.T) {
	got, err := Scale(7, Rational{Num: 1, Den: 3}, Rational{Num: 1, Den: 1}, "x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 2 {
		t.Fatalf("truncation failed: got %d want 2", got)
	}
}

func TestScaleOverflow(t *testing.T) {
	_, err := Scale(math.MaxUint64, Rational{Num: 2, Den: 1}, Rational{Num: 3, Den: 1}, "dts[9]")
	if err == nil {
		t.Fatal("expected overflow error")
	}
	fe, ok := fault.AsError(err)
	if !ok || fe.Kind != fault.KindOverflow || fe.Field != "dts[9]" {
		t.Fatalf("error not locatable: %v", err)
	}
}

func TestScaleZeroDenominator(t *testing.T) {
	if _, err := NewRational(1, 0, "timescale"); err == nil {
		t.Fatal("expected zero-denominator rejection")
	}
	if _, err := Scale(1, Rational{Num: 1, Den: 0}, Rational{Num: 1, Den: 1}, "x"); err == nil {
		t.Fatal("expected error for zero input denominator")
	}
}

func TestToRTPTimestampWrapsMod32(t *testing.T) {
	// 90 kHz video clock: 80000 ticks -> wrap to 80000-65536.
	d := Duration{Value: 80000, Rate: Rational{Num: 1, Den: 90000}}
	got, err := ToRTPTimestamp(d, 90000, "rtp_ts")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != uint32(80000) {
		t.Fatalf("got %d want 80000", got)
	}
	big := Duration{Value: 1 << 35, Rate: Rational{Num: 1, Den: 1}}
	if _, err := ToRTPTimestamp(big, 1, "rtp_ts"); err != nil {
		t.Fatalf("modular wrap must not error: %v", err)
	}
}

func TestAddOverflow(t *testing.T) {
	a := Duration{Value: math.MaxUint64, Rate: Rational{Num: 1, Den: 1}}
	b := Duration{Value: 1, Rate: Rational{Num: 1, Den: 1}}
	if _, err := Add(a, b, "duration"); err == nil {
		t.Fatal("expected overflow on add")
	}
	if _, err := Add(a, Duration{Value: 1, Rate: Rational{Num: 2, Den: 1}}, "duration"); err == nil {
		t.Fatal("expected error for mismatched rates")
	}
}
