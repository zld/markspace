package timeline_test

import (
	"math"
	"testing"

	"mediacheck/internal/report"
	"mediacheck/internal/timeline"
)

func TestMatroskaClockConvertsExplicitTimebase(t *testing.T) {
	clock, err := timeline.NewMatroskaClock(1_000_000, math.MaxInt64)
	if err != nil {
		t.Fatal(err)
	}
	got, serr := clock.BlockNS(1, 10, 100, 140)
	if serr != nil {
		t.Fatal(serr)
	}
	if want := int64(11_000_000); got != want {
		t.Fatalf("BlockNS=%d want %d", got, want)
	}

	got, serr = clock.BlockNS(100, -10, 100, 140)
	if serr != nil {
		t.Fatal(serr)
	}
	if want := int64(90_000_000); got != want {
		t.Fatalf("negative relative BlockNS=%d want %d", got, want)
	}
}

func TestMatroskaClockRejectsZeroTimebaseAndNegativeTimestamp(t *testing.T) {
	if _, err := timeline.NewMatroskaClock(0, 0); err == nil {
		t.Fatal("expected bad timebase error")
	}
	clock, err := timeline.NewMatroskaClock(1_000_000, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, serr := clock.BlockNS(0, -1, 7, 9)
	if serr == nil || serr.Record.ErrorCode != report.TimelineOverflow {
		t.Fatalf("expected overflow/negative structured error, got %#v", serr)
	}
}

func TestRTPDeltaNSUsesClockRate(t *testing.T) {
	// 90000 ticks per second: 2970 ticks is exactly 33 ms.
	got, err := timeline.RTPDeltaNS(2970, 90_000)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(33_000_000); got != want {
		t.Fatalf("RTPDeltaNS=%d want %d", got, want)
	}

	// 48000 ticks per second: 960 ticks is exactly 20 ms.
	got, err = timeline.RTPDeltaNS(960, 48_000)
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(20_000_000); got != want {
		t.Fatalf("RTPDeltaNS=%d want %d", got, want)
	}
}

func TestUnwrapperExpands32BitWrap(t *testing.T) {
	u, err := timeline.NewUnwrapper(32, 10_000)
	if err != nil {
		t.Fatal(err)
	}
	first, err := u.Unwrap(math.MaxUint32 - 100)
	if err != nil {
		t.Fatal(err)
	}
	next, err := u.Unwrap(100)
	if err != nil {
		t.Fatal(err)
	}
	if next <= first {
		t.Fatalf("wrapped timestamp did not expand forward: first=%d next=%d", first, next)
	}
	if delta := next - first; delta != 201 {
		t.Fatalf("expanded wrap delta=%d want 201", delta)
	}
}

func TestUnwrapperRejectsNonUniqueHalfRange(t *testing.T) {
	// 8-bit counter; a jump of exactly 128 has two equal modulo expansions.
	u, err := timeline.NewUnwrapper(8, 128)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Unwrap(10); err != nil {
		t.Fatal(err)
	}
	_, serr := u.Unwrap(138) // 10 + 128 mod 256
	if serr == nil {
		t.Fatal("expected ambiguous wrap failure")
	}
	if code := serr.Record.ErrorCode; code != report.TimelineWrapAmbiguous {
		t.Fatalf("code=%q want %q", code, report.TimelineWrapAmbiguous)
	}
}

func TestUnwrapperRejectsJumpOutsideBound(t *testing.T) {
	u, err := timeline.NewUnwrapper(8, 10)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := u.Unwrap(10); err != nil {
		t.Fatal(err)
	}
	_, serr := u.Unwrap(200)
	if serr == nil || serr.Record.ErrorCode != report.TimelineWrapAmbiguous {
		t.Fatalf("expected bounded jump failure, got %#v", serr)
	}
}
