package media

import (
	"math"
	"testing"
)

func TestScaleTicks(t *testing.T) {
	got, err := ScaleTicks(48000, 48000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1_000_000_000 {
		t.Fatalf("48k/48k = %d ns, want 1s", got)
	}

	got, err = ScaleTicks(90000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 1_000_000_000 {
		t.Fatalf("90k/90k = %d ns, want 1s", got)
	}

	if _, err := ScaleTicks(1, 0); err == nil {
		t.Fatal("expected zero timebase error")
	} else if me, ok := AsError(err); !ok || me.Stage != StageTimeline {
		t.Fatalf("expected structured timeline error, got %T %v", err, err)
	}

	if _, err := ScaleTicks(math.MaxUint64, 1); err == nil {
		t.Fatal("expected multiplication overflow")
	}
}

func TestCheckedAddOverflow(t *testing.T) {
	if _, err := CheckedAdd(math.MaxInt64, 1); err == nil {
		t.Fatal("expected positive overflow")
	}
	if _, err := CheckedAdd(math.MinInt64, -1); err == nil {
		t.Fatal("expected negative overflow")
	}
	got, err := CheckedAdd(1_000, 2_000)
	if err != nil || got != 3_000 {
		t.Fatalf("CheckedAdd = %d, %v", got, err)
	}
}

func TestMatroskaTimecodeScale(t *testing.T) {
	// Default TimecodeScale is 1ms represented as 1,000,000 ns.
	got, err := ScaleMatroskaTimecode(25, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 25_000_000 {
		t.Fatalf("timecode 25 = %d ns, want 25ms", got)
	}

	got, err = ScaleMatroskaTimecode(-4, 1_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if got != -4_000_000 {
		t.Fatalf("relative timecode -4 = %d ns, want -4ms", got)
	}

	if _, err := ScaleMatroskaTimecode(math.MaxInt64, 1_000_000); err == nil {
		t.Fatal("expected timecode overflow")
	}
}
