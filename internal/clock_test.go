package rtpreplay

import (
	"errors"
	"testing"
)

func TestScaleExactAndOverflow(t *testing.T) {
	got, err := Scale(48000, 48000, 1000)
	if err != nil || got != 1000 {
		t.Fatalf("Scale(48000,48000,1000)=%d,%v want 1000,nil", got, err)
	}
	if _, err := Scale(1<<63, 48000, 1); err == nil {
		t.Fatal("expected overflow error")
	}
	var se *Error
	if err := AsError(func() error { _, e := Scale(1<<63, 48000, 1); return e }()); !errors.As(err, &se) || se.Code != CodeTimeOverflow {
		t.Fatalf("want CodeTimeOverflow, got %v", err)
	}
}

func TestMillisTruncatesTowardZero(t *testing.T) {
	// 1 tick at 1kHz is exactly 1ms; 1 tick at 3kHz truncates to 0ms.
	if v, _ := Millis(1, 1000); v != 1 {
		t.Fatalf("Millis(1,1000)=%d", v)
	}
	if v, _ := Millis(1, 3000); v != 0 {
		t.Fatalf("Millis(1,3000)=%d", v)
	}
	if v, _ := Millis(7, 3000); v != 2 {
		t.Fatalf("Millis(7,3000)=%d", v)
	}
}

func TestNanosRoundTripForCommonRates(t *testing.T) {
	for _, rate := range []uint32{48000, 44100, 90000, 1000} {
		ticks, err := DurationTicks(1000, rate)
		if err != nil {
			t.Fatal(err)
		}
		ns, err := TicksToNanos(ticks, rate)
		if err != nil {
			t.Fatal(err)
		}
		if ns != 1_000_000_000 {
			t.Fatalf("rate=%d 1000ms -> %dns", rate, ns)
		}
		back, err := NanosToTicks(ns, rate)
		if err != nil {
			t.Fatal(err)
		}
		if back != ticks {
			t.Fatalf("rate=%d round trip %d != %d", rate, back, ticks)
		}
	}
}

func TestTimebaseValidation(t *testing.T) {
	if _, err := NewTimebase(0); err == nil {
		t.Fatal("expected error for zero rate")
	}
	tb, err := NewTimebase(48000)
	if err != nil || tb.String() != "48000/1" {
		t.Fatalf("tb=%v err=%v", tb, err)
	}
}
