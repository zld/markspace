package clock

import (
	"errors"
	"math"
	"testing"
)

func TestRationalValidation(t *testing.T) {
	if _, err := New(1, 0); !errors.Is(err, ErrInvalidRational) {
		t.Fatalf("zero denominator: want ErrInvalidRational, got %v", err)
	}
	r, err := New(3, -2)
	if err != nil {
		t.Fatalf("normalizing sign: %v", err)
	}
	if r.Num != -3 || r.Den != 2 {
		t.Fatalf("sign normalization = %+v, want {-3 2}", r)
	}
}

func TestNanosTicksRoundTrip(t *testing.T) {
	// Sub-tick nanosecond values (1, 999) truncate to zero, which is expected;
	// start at one full 90 kHz tick (~11111 ns).
	cases := []int64{0, 11112, 1_000_000_000, 20_000_000, 12_345_678_901}
	for _, nanos := range cases {
		ticks, err := NanosToTicks(nanos, MediaClock)
		if err != nil {
			t.Fatalf("NanosToTicks(%d): %v", nanos, err)
		}
		back, err := TicksToNanos(ticks, MediaClock)
		if err != nil {
			t.Fatalf("TicksToNanos(%d): %v", ticks, err)
		}
		// 90 kHz granularity: truncation on each direction can drift by at
		// most one tick (~11111 ns).
		d := back - nanos
		if d < 0 {
			d = -d
		}
		if d > 12000 {
			t.Fatalf("round trip %d -> %d -> %d, drift %d", nanos, ticks, back, d)
		}
	}
}

func TestRescaleKnownValues(t *testing.T) {
	// 1 s on the 90 kHz clock equals 48000 ticks at 48 kHz.
	got, err := Rescale(90000, MediaClock, Rational{Num: 48000, Den: 1})
	if err != nil || got != 48000 {
		t.Fatalf("Rescale 90000 @90k -> @48k = %d, %v", got, err)
	}
	// 1.5 s at 48 kHz -> 135000 ticks at 90 kHz.
	got, err = Rescale(72000, Rational{Num: 48000, Den: 1}, MediaClock)
	if err != nil || got != 135000 {
		t.Fatalf("Rescale 72000 @48k -> @90k = %d, %v", got, err)
	}
}

func TestRTPTimestamp(t *testing.T) {
	ts, err := RTPTimestamp(1_000_000_000, MediaClock)
	if err != nil || ts != 90000 {
		t.Fatalf("RTPTimestamp(1s) = %d, %v", ts, err)
	}
	// Values that would wrap uint32 must fail explicitly.
	if _, err := RTPTimestamp(math.MaxInt64, MediaClock); !errors.Is(err, ErrOverflow) {
		t.Fatalf("huge timestamp: want ErrOverflow, got %v", err)
	}
	// A capture time one full media tick before zero yields a negative tick.
	if _, err := RTPTimestamp(-1_000_000_000, MediaClock); !errors.Is(err, ErrOverflow) {
		t.Fatalf("negative timestamp: want ErrOverflow, got %v", err)
	}
}

func TestOverflowInt64(t *testing.T) {
	huge := Rational{Num: math.MaxInt64, Den: 1}
	if _, err := NanosToTicks(math.MaxInt64, huge); !errors.Is(err, ErrOverflow) {
		t.Fatalf("NanosToTicks overflow: want ErrOverflow, got %v", err)
	}
	// ticks * Den * 1e9 / Num stays inside int64 range here, so assert the
	// exact quotient instead of an overflow.
	got, err := TicksToNanos(math.MaxInt64, huge)
	if err != nil || got != NanosPerSecond {
		t.Fatalf("TicksToNanos quotient = %d, %v; want %d, nil", got, err, NanosPerSecond)
	}
	if _, err := Rescale(math.MaxInt64, Rational{Num: 1, Den: 1}, Rational{Num: math.MaxInt64, Den: 1}); !errors.Is(err, ErrOverflow) {
		t.Fatalf("Rescale overflow: want ErrOverflow, got %v", err)
	}
}
