package clock

import (
	"errors"
	"math"
	"testing"
)

func TestToNanos(t *testing.T) {
	cases := []struct {
		name  string
		ticks int64
		rate  int64
		want  int64
		err   error
	}{
		{"90k one tick", 1, 90000, 11111, nil},
		{"48k 48 ticks", 48, 48000, 1_000_000, nil},
		{"25fps one frame", 1, 25, 40_000_000, nil},
		{"zero rate", 1, 0, 0, ErrInvalidTick},
		{"negative rate", 1, -8, 0, ErrInvalidTick},
		{"overflow", math.MaxInt64 / 2, 1, 0, ErrOverflow},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ToNanos(tc.ticks, tc.rate)
			if !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
			if got != tc.want {
				t.Fatalf("got %d, want %d", got, tc.want)
			}
		})
	}
}

func TestFromNanos(t *testing.T) {
	cases := []struct {
		nanos int64
		rate  int64
		want  int64
	}{
		{40_000_000, 90000, 3600},
		{1_000_000_000, 48000, 48000},
		{11111, 90000, 1},
		{0, 90000, 0},
	}
	for _, tc := range cases {
		got, err := FromNanos(tc.nanos, tc.rate)
		if err != nil {
			t.Fatalf("FromNanos(%d): %v", tc.nanos, err)
		}
		if got != tc.want {
			t.Fatalf("FromNanos(%d, %d) = %d, want %d", tc.nanos, tc.rate, got, tc.want)
		}
	}
	if _, err := FromNanos(1, 0); !errors.Is(err, ErrInvalidTick) {
		t.Fatalf("bad rate: %v", err)
	}
	if _, err := FromNanos(math.MaxInt64, 1_000_000); !errors.Is(err, ErrOverflow) {
		t.Fatalf("overflow not detected: %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	for _, nanos := range []int64{0, 1, 999, 40_000_000, 1_234_567_890} {
		ticks, err := FromNanos(nanos, 90000)
		if err != nil {
			t.Fatalf("FromNanos: %v", err)
		}
		back, err := ToNanos(ticks, 90000)
		if err != nil {
			t.Fatalf("ToNanos: %v", err)
		}
		if d := back - nanos; d < -11112 || d > 11112 {
			t.Fatalf("round trip drift %d for %d", d, nanos)
		}
	}
}

func TestAddSubOverflow(t *testing.T) {
	if _, err := Add(math.MaxInt64, 1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("max+1 = %v", err)
	}
	if _, err := Add(math.MinInt64, -1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("min-1 = %v", err)
	}
	if _, err := Sub(math.MinInt64, 1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("min-1 sub = %v", err)
	}
	if v, err := Add(100, -5); err != nil || v != 95 {
		t.Fatalf("add got %d %v", v, err)
	}
}

func TestCheckMonotonic(t *testing.T) {
	if err := CheckMonotonic(5, 5); err != nil {
		t.Fatal(err)
	}
	if err := CheckMonotonic(6, 5); err == nil {
		t.Fatal("backward timestamp accepted")
	}
}
