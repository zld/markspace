package timebase

import (
	"errors"
	"math"
	"testing"
)

func TestSampleTickRoundTrip(t *testing.T) {
	ticks, err := SamplesToTicks(48000, 48000, TickRate)
	if err != nil {
		t.Fatal(err)
	}
	if ticks != int64(TickRate) {
		t.Fatalf("one second = %d ticks, want %d", ticks, TickRate)
	}
	n, err := TicksToSamples(ticks, TickRate, 48000)
	if err != nil || n != 48000 {
		t.Fatalf("round trip = %d, %v", n, err)
	}
}

func TestConversionOverflow(t *testing.T) {
	if _, err := SamplesToTicks(math.MaxInt64, 48000, TickRate); !errors.Is(err, CodeOverflow) {
		t.Fatalf("err=%v, want %s", err, CodeOverflow)
	}
	if _, ok := CheckedMul(math.MaxInt64, 2); ok {
		t.Fatal("overflow multiplication reported success")
	}
	if v, ok := CheckedMul(-7, 6); !ok || v != -42 {
		t.Fatalf("CheckedMul = %d,%v", v, ok)
	}
}

func TestStableErrorFields(t *testing.T) {
	err := At(CodeAlignment, 1234, "partial frame").WithTrack("cam-1").WithRange(10, 20)
	var te *Error
	if !errors.As(err, &te) {
		t.Fatal("not canonical error")
	}
	if te.Offset != 1234 || te.Track != "cam-1" || te.Range.Start != 10 || te.Range.End != 20 {
		t.Fatalf("fields not retained: %#v", te)
	}
	if !errors.Is(err, CodeAlignment) {
		t.Fatal("errors.Is does not match stable code")
	}
}

func TestRationalConvert(t *testing.T) {
	// 48 kHz samples to 96 kHz samples doubles the count.
	got, err := Convert(48000, Rational{Num: 1, Den: 48000}, Rational{Num: 1, Den: 96000})
	if err != nil {
		t.Fatal(err)
	}
	if got != 96000 {
		t.Fatalf("Convert = %d, want 96000", got)
	}
}
