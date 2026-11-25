package clock

import (
	"errors"
	"math"
	"testing"

	"mediacore/internal/diagnostic"
)

func TestConvertTicksExactAndRationalErrors(t *testing.T) {
	got, err := ConvertTicks(1800, 90000, UnifiedTimescale)
	if err != nil {
		t.Fatal(err)
	}
	if got != 20_000_000 {
		t.Fatalf("1800/90000s = %d ticks, want 20ms", got)
	}
	if _, err := ConvertTicks(1, 0, UnifiedTimescale); !errors.Is(err, diagnostic.ErrZeroDenominator) {
		t.Fatalf("zero from timescale error = %v", err)
	}
	if _, err := ConvertTicks(1, 90000, 0); !errors.Is(err, diagnostic.ErrZeroDenominator) {
		t.Fatalf("zero target scale error = %v", err)
	}
	if _, err := ConvertTicks(math.MaxInt64, 1, UnifiedTimescale); !errors.Is(err, diagnostic.ErrOverflow) {
		t.Fatalf("overflow error = %v", err)
	}
}

func TestTimelineAdvancesAcrossDiscontinuityWithoutReset(t *testing.T) {
	tl := NewTimeline()
	start, end, err := tl.Advance(40, 1000)
	if err != nil || start != 0 || end != 40_000_000 {
		t.Fatalf("first advance = %d,%d,%v", start, end, err)
	}
	start, end, err = tl.Advance(40, 1000)
	if err != nil || start != 40_000_000 || end != 80_000_000 {
		t.Fatalf("continuous advance = %d,%d,%v", start, end, err)
	}
	if _, _, err := tl.Advance(-1, 1000); !errors.Is(err, diagnostic.ErrTimeRegression) {
		t.Fatalf("negative duration error = %v", err)
	}
}

func TestAlignmentLimitIsTwentyMilliseconds(t *testing.T) {
	ok, d, err := AlignmentWithinLimit(20_000_000, 0)
	if err != nil || !ok || d != 20_000_000 {
		t.Fatalf("exact 20ms should pass: ok=%v d=%d err=%v", ok, d, err)
	}
	ok, _, err = AlignmentWithinLimit(20_000_001, 0)
	if err != nil || ok {
		t.Fatalf("just over 20ms should fail: ok=%v err=%v", ok, err)
	}
}
