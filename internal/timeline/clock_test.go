package timeline

import (
	"errors"
	"math"
	"testing"
)

func TestFromScaleRoundingHalfAwayFromZero(t *testing.T) {
	cases := []struct {
		name      string
		units     int64
		timescale uint32
		want      Time
	}{
		{"exact millisecond", 1, 1000, 1000},
		{"half rounds up positive", 1, 2000, 500},          // 0.5 ms -> 500 us
		{"below half rounds down", 1, 3000, 333},           // 333.333 us
		{"above half rounds up", 2, 3000, 667},              // 666.667 us
		{"half rounds away negative", -1, 2000, -500},       // -0.5 ms
		{"negative below half", -1, 3000, -333},             // -333.333 us
		{"non-zero base offset", 90_000 + 1, 90_000, 1_000_011},
		{"large exact", 1800, 1, 1_800_000_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := FromScale(tc.units, tc.timescale)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("FromScale(%d, %d) = %d, want %d", tc.units, tc.timescale, got, tc.want)
			}
		})
	}
}

func TestFromScaleRejectsBadTimescale(t *testing.T) {
	if _, err := FromScale(10, 0); Classify(err) != KindInvalidInput {
		t.Fatalf("zero timescale: got %v, want KindInvalidInput", err)
	}
	if _, err := FromScale(10, MaxTimescale+1); Classify(err) != KindLimit {
		t.Fatalf("oversized timescale: got %v, want KindLimit", err)
	}
}

func TestFromScaleRejectsOverflowAndRange(t *testing.T) {
	// int64 multiplication must be detected rather than wrap.
	if _, err := FromScale(math.MaxInt64/1000, 100_000); Classify(err) != KindLimit {
		t.Fatalf("overflow: got %v, want KindLimit", err)
	}
	// A 48h timestamp exceeds the 24h media window.
	if _, err := FromScale(48*3600, 1); Classify(err) != KindLimit {
		t.Fatalf("range: got %v, want KindLimit", err)
	}
}

func TestToScaleRoundTrip(t *testing.T) {
	for _, ts := range []uint32{90_000, 48_000, 44_100, 1000, 1} {
		original := int64(12_345) // 12,345 source units; well inside the 24h window
		axis, err := FromScale(original, ts)
		if err != nil {
			t.Fatalf("FromScale: %v", err)
		}
		back, err := ToScale(axis, ts)
		if err != nil {
			t.Fatalf("ToScale: %v", err)
		}
		// Round-trip error is bounded by one source unit.
		d := back - original
		if d < -1 || d > 1 {
			t.Fatalf("timescale %d round trip drifted %d -> %d", ts, original, back)
		}
	}
}

func TestToScaleRejectsZero(t *testing.T) {
	if _, err := ToScale(Second, 0); Classify(err) != KindInvalidInput {
		t.Fatalf("got %v, want KindInvalidInput", err)
	}
}

func TestRound(t *testing.T) {
	cases := []struct {
		t, step, want Time
	}{
		{1500, Millisecond, 2000},
		{1499, Millisecond, 1000},
		{-1500, Millisecond, -2000},
		{5*Second + 4*Millisecond, 10 * Millisecond, 5*Second + 0},
		{5*Second + 5*Millisecond, 10 * Millisecond, 5*Second + 10*Millisecond},
	}
	for _, tc := range cases {
		got, err := Round(tc.t, tc.step)
		if err != nil {
			t.Fatalf("Round: %v", err)
		}
		if got != tc.want {
			t.Fatalf("Round(%d,%d) = %d, want %d", tc.t, tc.step, got, tc.want)
		}
	}
	if _, err := Round(Second, 0); Classify(err) != KindInvalidInput {
		t.Fatalf("zero step: got %v, want KindInvalidInput", err)
	}
}

func TestAddSubOverflow(t *testing.T) {
	if _, err := Add(MaxTime, Hour); Classify(err) != KindLimit {
		t.Fatalf("add range: got %v", err)
	}
	if _, err := Sub(MinTime, Hour); Classify(err) != KindLimit {
		t.Fatalf("sub range: got %v", err)
	}
	got, err := Sub(10*Second, 4*Second)
	if err != nil || got != 6*Second {
		t.Fatalf("normal subtraction broken: %v %v", got, err)
	}
}

func TestErrorClassification(t *testing.T) {
	base := errors.New("disk on fire")
	e := WrapError(KindCorrupt, "index unreadable", base)
	var typed *Error
	if !errors.As(e, &typed) || typed.Kind != KindCorrupt {
		t.Fatalf("classification lost: %v", e)
	}
	if !errors.Is(e, base) {
		t.Fatal("wrapped cause not reachable via errors.Is")
	}
	if Classify(errors.New("plain")) != KindUnknown {
		t.Fatal("plain error should classify as KindUnknown")
	}
}
