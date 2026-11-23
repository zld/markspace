package model

import (
	"math"
	"testing"
)

func TestRationalNormalisation(t *testing.T) {
	r, err := NewRational(180000, 2)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != 90000 || r.Den != 1 {
		t.Fatalf("normalised to %d/%d", r.Num, r.Den)
	}
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("zero denominator accepted")
	} else if e := err.(*Error); e.Code != CodeZeroTimescale {
		t.Fatalf("code = %s", e.Code)
	}
	if _, err := NewRational(-1, 10); err == nil {
		t.Fatal("negative time base accepted")
	}
}

func TestTickConversionAccuracy(t *testing.T) {
	cases := []struct {
		ticks     uint64
		timescale uint32
		want      int64
	}{
		{90000, 90000, 1_000_000_000},
		{45000, 90000, 500_000_000},
		{1, 90000, 11111},
		{48000, 48000, 1_000_000_000},
		{0, 1000, 0},
	}
	for _, c := range cases {
		got, err := TicksToNanos(c.ticks, c.timescale)
		if err != nil {
			t.Fatalf("%d/%d: %v", c.ticks, c.timescale, err)
		}
		if got != c.want {
			t.Fatalf("%d/%d = %d, want %d", c.ticks, c.timescale, got, c.want)
		}
	}
	if _, err := TicksToNanos(1, 0); err == nil {
		t.Fatal("zero timescale accepted")
	}
}

func TestOverflowDetection(t *testing.T) {
	if _, err := TicksToNanos(math.MaxUint64, 1); err == nil {
		t.Fatal("overflow not detected")
	} else if e := err.(*Error); e.Code != CodeTimeOverflow {
		t.Fatalf("code = %s", e.Code)
	}
	if _, err := AddNanos(math.MaxInt64, 1); err == nil {
		t.Fatal("addition overflow not detected")
	}
	if _, err := DurationToNanos(0, 1000); err == nil {
		t.Fatal("zero duration accepted")
	}
}

func TestParseISODuration(t *testing.T) {
	cases := map[string]int64{
		"PT8S":        8 * NanosPerSecond,
		"PT2.5S":      2_500_000_000,
		"PT1H2M3S":    3723 * NanosPerSecond,
		"P1DT1S":      (24*3600 + 1) * NanosPerSecond,
		"PT0.000001S": 1000,
	}
	for in, want := range cases {
		got, err := ParseISODuration(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "8S", "P", "PT", "P1Y", "-PT8S", "PT8", "PT8X"} {
		if _, err := ParseISODuration(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestParseDecimalSeconds(t *testing.T) {
	cases := map[string]int64{
		"4":            4 * NanosPerSecond,
		"4.000":        4 * NanosPerSecond,
		"2.500":        2_500_000_000,
		"0.1":          100_000_000,
		"0.0000000005": 1, // rounds up at nanosecond precision
	}
	for in, want := range cases {
		got, err := ParseDecimalSeconds(in)
		if err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if got != want {
			t.Fatalf("%s = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"", "-1", "1e3", "abc", "1.2.3", "0"} {
		if _, err := ParseDecimalSeconds(bad); err == nil {
			t.Fatalf("%q accepted", bad)
		}
	}
}

func TestSortErrorsIsDeterministic(t *testing.T) {
	errs := []*Error{
		{Code: "b", Offset: 10},
		{Code: "a", Offset: 10},
		{Code: "c", Offset: NoOffset},
		{Code: "d", Offset: 2},
	}
	SortErrors(errs)
	want := []Code{"d", "a", "b", "c"}
	for i, e := range errs {
		if e.Code != want[i] {
			t.Fatalf("position %d = %s, want %s", i, e.Code, want[i])
		}
	}
}
