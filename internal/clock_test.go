package continuity

import (
	"errors"
	"math"
	"testing"
)

func TestRescaleExactAndRounding(t *testing.T) {
	cases := []struct {
		v        int64
		from, to uint32
		want     int64
	}{
		{48000, 48000, 90000, 90000},
		{1920, 48000, 90000, 3600},
		{90000, 90000, 1000, 1000},
		{1, 90000, 1000, 0},  // 0.011ms rounds down
		{1, 1000, 90000, 90}, // exact
		{1, 2, 3, 2},         // 1.5 rounds away from zero
		{-1, 2, 3, -2},       // negative half rounds away from zero
		{0, 90000, 48000, 0},
		{math.MaxInt64, 1, 1, math.MaxInt64},
	}
	for _, tc := range cases {
		got, err := Rescale(tc.v, tc.from, tc.to)
		if err != nil {
			t.Fatalf("Rescale(%d,%d,%d): %v", tc.v, tc.from, tc.to, err)
		}
		if got != tc.want {
			t.Errorf("Rescale(%d,%d,%d)=%d want %d", tc.v, tc.from, tc.to, got, tc.want)
		}
	}
}

func TestRescaleRejectsZeroTimescaleAndOverflow(t *testing.T) {
	if _, err := Rescale(1, 0, 90000); !errors.Is(err, ErrZeroTimescale) {
		t.Fatalf("want ErrZeroTimescale, got %v", err)
	}
	if _, err := Rescale(1, 90000, 0); !errors.Is(err, ErrZeroTimescale) {
		t.Fatalf("want ErrZeroTimescale, got %v", err)
	}
	if _, err := Rescale(math.MaxInt64, 1, 90000); !errors.Is(err, ErrTickOverflow) {
		t.Fatalf("want ErrTickOverflow, got %v", err)
	}
}

func TestParseDecimalTicks(t *testing.T) {
	cases := []struct {
		in   string
		want Ticks
	}{
		{"1", 90000},
		{"1.0", 90000},
		{"0.5", 45000},
		{"2.000", 180000},
		{"0.000011", 1}, // 0.99 ticks rounds to 1
		{"0.000005", 0}, // 0.45 ticks rounds to 0
		{"+1.5", 135000},
	}
	for _, tc := range cases {
		got, err := ParseDecimalTicks(tc.in, BaseTimescale)
		if err != nil {
			t.Fatalf("ParseDecimalTicks(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("ParseDecimalTicks(%q)=%d want %d", tc.in, got, tc.want)
		}
	}
	for _, bad := range []string{"", "-1", "1.", ".5", "1e3", "1.2.3", "0.1234567890", "abc"} {
		if _, err := ParseDecimalTicks(bad, BaseTimescale); err == nil {
			t.Errorf("ParseDecimalTicks(%q) unexpectedly succeeded", bad)
		}
	}
}

func TestMillisRoundTrip(t *testing.T) {
	tk, err := FromMillis(500)
	if err != nil {
		t.Fatal(err)
	}
	if tk != 45000 {
		t.Fatalf("FromMillis(500)=%d", tk)
	}
	if tk.Millis() != 500 {
		t.Fatalf("Millis=%d", tk.Millis())
	}
	if Ticks(-45000).Millis() != -500 {
		t.Fatalf("negative Millis=%d", Ticks(-45000).Millis())
	}
}

func TestUnwrapTimestamp(t *testing.T) {
	const mod = int64(1) << 32
	cases := []struct{ v, ref, want int64 }{
		{1000, 900, 1000},
		{1, mod - 100, mod + 1}, // wrapped forward
		{mod - 1, 2, -1},        // wrapped backward
		{5, 0, 5},
		{0, 5, 0},
	}
	for _, tc := range cases {
		got, err := UnwrapTimestamp(tc.v, tc.ref, mod)
		if err != nil {
			t.Fatalf("UnwrapTimestamp(%d,%d): %v", tc.v, tc.ref, err)
		}
		if got != tc.want {
			t.Errorf("UnwrapTimestamp(%d,%d)=%d want %d", tc.v, tc.ref, got, tc.want)
		}
	}
}

func TestSpan(t *testing.T) {
	s, err := NewSpan(10, 20)
	if err != nil {
		t.Fatal(err)
	}
	if s.Duration() != 10 || !s.Contains(10) || s.Contains(20) {
		t.Fatalf("span semantics broken: %+v", s)
	}
	zero, err := NewSpan(10, 10)
	if err != nil {
		t.Fatal(err)
	}
	if zero.Overlaps(s) || s.Overlaps(zero) {
		t.Fatal("zero-length span must not overlap")
	}
	if _, err := NewSpan(20, 10); err == nil {
		t.Fatal("inverted span must fail")
	}
	if got := s.GapTo(Span{Start: 30, End: 40}); got != 10 {
		t.Fatalf("GapTo=%d", got)
	}
	if got := s.GapTo(Span{Start: 15, End: 18}); got != 0 {
		t.Fatalf("overlap GapTo=%d", got)
	}
}
