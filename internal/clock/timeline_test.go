package clock

import (
	"testing"
)

func TestToNanosExactGrids(t *testing.T) {
	cases := []struct {
		scale uint32
		ticks int64
		want  Nanos
	}{
		{90000, 90000, Nanos(TicksPerSecond)},
		{90000, 3003, 33_366_667}, // 3003/90000 s, half-away rounding
		{48000, 1024, 21_333_333},
		{1000, 0, 0},
		{1000, -5, -5_000_000},
	}
	for _, c := range cases {
		b := TimeBase{TrackID: 1, Timescale: c.scale}
		got, err := b.ToNanos(c.ticks)
		if err != nil {
			t.Fatalf("ToNanos(%d): %v", c.ticks, err)
		}
		if got != c.want {
			t.Errorf("ToNanos scale=%d ticks=%d = %d, want %d", c.scale, c.ticks, got, c.want)
		}
		// Round trip on the exact grid whenever the nanos value divides.
		back, err := b.ToTicks(got)
		if err != nil {
			t.Fatalf("ToTicks: %v", err)
		}
		if back != c.ticks {
			t.Errorf("roundtrip ticks=%d -> %d -> %d", c.ticks, got, back)
		}
	}
}

func TestToNanosRoundingHalfAway(t *testing.T) {
	b := TimeBase{Timescale: 3}
	// 1 tick = 333333333.333... ns -> 333333333 (remainder below half)
	if got, _ := b.ToNanos(1); got != 333_333_333 {
		t.Fatalf("got %d", got)
	}
	// 2 ticks = 666666666.666... ns -> 666666667 (above half)
	if got, _ := b.ToNanos(2); got != 666_666_667 {
		t.Fatalf("got %d", got)
	}
	// negative symmetry
	if got, _ := b.ToNanos(-2); got != -666_666_667 {
		t.Fatalf("got %d", got)
	}
}

func TestOverflowAndZeroScale(t *testing.T) {
	b := TimeBase{Timescale: 1}
	if _, err := b.ToNanos(1 << 62); Classify(err) != KindRecoverable {
		t.Fatalf("want overflow error, got %v", err)
	}
	if _, err := b.DeltaNanos(1 << 63); Classify(err) != KindRecoverable {
		t.Fatalf("want recoverable, got %v", err)
	}
	bad := TimeBase{Timescale: 0}
	if _, err := bad.ToNanos(10); Classify(err) != KindInvalidArgument {
		t.Fatalf("want arg error, got %v", err)
	}
}

func TestExitCodes(t *testing.T) {
	if KindRecoverable.ExitCode() != 2 || KindInvalidArgument.ExitCode() != 3 ||
		KindInvariant.ExitCode() != 4 || KindCanceled.ExitCode() != 2 {
		t.Fatal("exit code mapping changed")
	}
}

