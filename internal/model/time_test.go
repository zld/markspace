package model

import (
	"math"
	"testing"
)

func TestTicksToMicrosExact(t *testing.T) {
	cases := []struct {
		ticks     uint64
		timescale uint32
		want      Micros
	}{
		{0, 90000, 0},
		{90000, 90000, 1_000_000},
		{48000, 48000, 1_000_000},
		{1, 90000, 11},  // 11.11us -> 11
		{1, 3, 333_333}, // 333333.33 -> 333333
		{1, 2, 500_000}, // exactly half rounds up
		{1, 8, 125_000}, // exact
		{450000, 90000, 5_000_000},
		{270000, 90000, 3_000_000},
	}
	for _, c := range cases {
		got, err := TicksToMicros(c.ticks, c.timescale)
		if err != nil {
			t.Fatalf("TicksToMicros(%d,%d): %v", c.ticks, c.timescale, err)
		}
		if got != c.want {
			t.Errorf("TicksToMicros(%d,%d)=%d, want %d", c.ticks, c.timescale, got, c.want)
		}
	}
}

func TestTicksToMicrosNoDrift(t *testing.T) {
	// Summing per-sample conversions must equal the conversion of the summed
	// ticks when every sample duration divides the timescale exactly.
	const ts uint32 = 90000
	const per = 1800 // 20ms
	var total Micros
	for i := 0; i < 5000; i++ {
		m, err := TicksToMicros(per, ts)
		if err != nil {
			t.Fatal(err)
		}
		total += m
	}
	want, err := TicksToMicros(per*5000, ts)
	if err != nil {
		t.Fatal(err)
	}
	if total != want {
		t.Fatalf("accumulated %d us drifts from exact %d us", total, want)
	}
}

func TestTicksToMicrosRejectsZeroTimescale(t *testing.T) {
	_, err := TicksToMicros(1, 0)
	te, ok := AsTaskError(err)
	if !ok || te.Category != CategoryZeroTimescale {
		t.Fatalf("want zero_timescale TaskError, got %v", err)
	}
}

func TestTicksToMicrosOverflow(t *testing.T) {
	if _, err := TicksToMicros(math.MaxUint64, 1000); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestRangeOfSaturates(t *testing.T) {
	r := RangeOf(math.MaxUint64-1, 100)
	if r.End != math.MaxUint64 {
		t.Fatalf("expected saturation at MaxUint64, got %d", r.End)
	}
	if !r.Valid() {
		t.Fatal("saturated range must stay valid")
	}
}

func TestTaskErrorCarriesLocation(t *testing.T) {
	te := NewTaskError(CategoryTruncatedBox, "ingest", "bad").WithOffset(42).WithTrack(7)
	if te.Offset != 42 || te.TrackID != 7 {
		t.Fatalf("location not attached: %+v", te)
	}
	if te.Error() == "" {
		t.Fatal("empty error string")
	}
}

func TestAbsMicros(t *testing.T) {
	if got := AbsMicros(10, 3); got != 7 {
		t.Fatalf("got %d", got)
	}
	if got := AbsMicros(3, 10); got != 7 {
		t.Fatalf("got %d", got)
	}
}
