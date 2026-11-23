package clock_test

import (
	"errors"
	"math"
	"testing"

	"mediacore/internal/clock"
)

func TestRescaleIsExactAndInteger(t *testing.T) {
	cases := []struct {
		ticks    int64
		from, to uint32
		want     int64
	}{
		{90000, 90000, 90000, 90000},
		{48000, 48000, 90000, 90000},
		{1024, 48000, 90000, 1920},
		{3000, 90000, 48000, 1600},
		{1, 48000, 90000, 1}, // 1.875 truncates toward zero, never rounds up
		{5, 3, 3, 5},
		{0, 1, 1, 0},
	}
	for _, tc := range cases {
		got, err := clock.Rescale(tc.ticks, tc.from, tc.to)
		if err != nil {
			t.Fatalf("Rescale(%d,%d,%d): %v", tc.ticks, tc.from, tc.to, err)
		}
		if got != tc.want {
			t.Errorf("Rescale(%d,%d,%d)=%d want %d", tc.ticks, tc.from, tc.to, got, tc.want)
		}
	}
}

func TestRescaleAccumulationMatchesExactSum(t *testing.T) {
	// A thousand audio ticks at 48 kHz must map to exactly the same value as
	// one call on their sum. Any float accumulation would drift here.
	const timescale = 48000
	const target = 90000
	var sequential int64
	for i := 0; i < 1000; i++ {
		v, err := clock.Rescale(1024, timescale, target)
		if err != nil {
			t.Fatal(err)
		}
		sequential += v
	}
	exact, err := clock.Rescale(1024*1000, timescale, target)
	if err != nil {
		t.Fatal(err)
	}
	if sequential != exact {
		t.Fatalf("accumulated %d != exact %d", sequential, exact)
	}
}

func TestRescaleRejectsInvalid(t *testing.T) {
	if _, err := clock.Rescale(1, 0, 90000); !errors.Is(err, clock.ErrTimescale) {
		t.Errorf("zero source timescale: %v", err)
	}
	if _, err := clock.Rescale(1, 90000, 0); !errors.Is(err, clock.ErrTimescale) {
		t.Errorf("zero target timescale: %v", err)
	}
	if _, err := clock.Rescale(math.MaxInt64, 1, math.MaxUint32); !errors.Is(err, clock.ErrOverflow) {
		t.Errorf("expected overflow, got %v", err)
	}
	if _, err := clock.Rescale(math.MinInt64, 3, math.MaxUint32); !errors.Is(err, clock.ErrOverflow) {
		t.Errorf("expected negative overflow, got %v", err)
	}
}

func TestDurationRejectsNegative(t *testing.T) {
	if _, err := clock.Duration(10, 5); !errors.Is(err, clock.ErrNegative) {
		t.Fatalf("expected negative duration, got %v", err)
	}
	if d, err := clock.Duration(5, 10); err != nil || d != 5 {
		t.Fatalf("Duration(5,10)=%d,%v", d, err)
	}
}

func TestMappingPreservesNonZeroStart(t *testing.T) {
	m, err := clock.NewMapping(2, 48000, 90000, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if !m.NonZeroStart() {
		t.Fatal("non-zero start not recorded")
	}
	if m.TimelineStart != 90000 {
		t.Fatalf("mapped start %d want 90000", m.TimelineStart)
	}
	got, err := m.ToTimeline(96000)
	if err != nil || got != 180000 {
		t.Fatalf("ToTimeline=%d,%v", got, err)
	}
	if _, err := m.ToTimeline(-1); !errors.Is(err, clock.ErrNegative) {
		t.Fatalf("negative ticks accepted: %v", err)
	}
	if _, err := clock.NewMapping(1, 0, 90000, 0); !errors.Is(err, clock.ErrTimescale) {
		t.Fatalf("zero timescale accepted: %v", err)
	}
}

func TestRationalNormalises(t *testing.T) {
	r, err := clock.New(6, -4)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != -3 || r.Den != 2 {
		t.Fatalf("New(6,-4)=%+v want {-3 2}", r)
	}
	if _, err := clock.New(1, 0); !errors.Is(err, clock.ErrDenominator) {
		t.Fatalf("zero denominator accepted: %v", err)
	}
}
