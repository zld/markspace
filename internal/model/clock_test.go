package model

import (
	"math"
	"strings"
	"testing"
)

func TestToNanosRounding(t *testing.T) {
	cases := []struct {
		value     int64
		timescale uint32
		want      Nanos
	}{
		{0, 1000, 0},
		{1000, 1000, Nanos(NanosPerSecond)},
		{1, 1000, Nanos(NanosPerMilli)},
		{1, 3, 333_333_333},
		{2, 3, 666_666_667},
		{-1, 1000, Nanos(-NanosPerMilli)},
		{90_000, 90_000, Nanos(NanosPerSecond)},
	}
	for _, c := range cases {
		got, err := ToNanos(c.value, c.timescale)
		if err != nil {
			t.Fatalf("ToNanos(%d,%d): %v", c.value, c.timescale, err)
		}
		if got != c.want {
			t.Errorf("ToNanos(%d,%d) = %d, want %d", c.value, c.timescale, got, c.want)
		}
	}
}

func TestToNanosRejectsZeroTimescale(t *testing.T) {
	if _, err := ToNanos(1, 0); err == nil {
		t.Fatal("expected error for zero timescale")
	}
}

func TestToNanosOverflow(t *testing.T) {
	_, err := ToNanos(math.MaxInt64, 1)
	if err == nil || !strings.Contains(err.Error(), "overflow") {
		t.Fatalf("expected overflow error, got %v", err)
	}
}

func TestNanosToScale(t *testing.T) {
	ns := Nanos(NanosPerSecond)
	got, err := NanosToScale(ns, 90_000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 90_000 {
		t.Errorf("NanosToScale = %d, want 90000", got)
	}
	// A large timestamp must not be truncated to zero by integer division.
	got, err = NanosToScale(Nanos(5*NanosPerSecond+NanosPerMilli), 48_000)
	if err != nil {
		t.Fatal(err)
	}
	if got < 240_000 || got > 240_049 {
		t.Errorf("NanosToScale sub-second = %d, want about 240048", got)
	}
}

func TestNanosMS(t *testing.T) {
	if got := Nanos(20 * NanosPerMilli).MS(); got != 20 {
		t.Errorf("MS = %v, want 20", got)
	}
}

func TestIntervalString(t *testing.T) {
	if got := (Interval{Start: 1, End: 2}).String(); got != "[1,2)" {
		t.Errorf("String = %q", got)
	}
}

func TestSamplesDigestStable(t *testing.T) {
	rng := NewRNG(7)
	var a, b uint64
	for i := 0; i < 10; i++ {
		x := rng.Next()
		if i == 0 {
			a = x
		}
		b = x
	}
	if a == b {
		t.Error("RNG produced a constant sequence")
	}
	if NewRNG(7).Next() != a {
		t.Error("RNG is not deterministic for the same seed")
	}
}
