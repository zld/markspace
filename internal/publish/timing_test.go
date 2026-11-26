package publish

import (
	"testing"

	"regress/internal/fault"
)

func TestRepresentationDurationWithinOneTick(t *testing.T) {
	// Two renditions at 24 fps whose totals differ by a single frame slot
	// (1/24 s) are exactly at the tolerance boundary and must pass.
	reps := []RepresentationTiming{
		{Name: "high", TotalTicks: 24576, Timescale: 12288},
		{Name: "low", TotalTicks: 24576, Timescale: 12288},
	}
	if err := CheckRepresentationDurations(24, reps); err != nil {
		t.Fatalf("identical durations rejected: %v", err)
	}

	// A two-tick drift at the source clock exceeds the one-tick tolerance.
	// 2/24 s in a 12288 timescale = 1024 ticks.
	drifted := append([]RepresentationTiming(nil), reps...)
	drifted[1] = RepresentationTiming{Name: "low", TotalTicks: 24576 + 1024, Timescale: 12288}
	err := CheckRepresentationDurations(24, drifted)
	if err == nil || !fault.HasCode(err, fault.CodeBoundaryDiscontinuity) {
		t.Fatalf("want boundary discontinuity for >1 tick drift, got %v", err)
	}
}

func TestTotalDurationTicks(t *testing.T) {
	entries := []ManifestEntry{
		{DurationTicks: 6144}, {DurationTicks: 6144},
		{DurationTicks: 6144}, {DurationTicks: 6144},
	}
	if got := TotalDurationTicks(entries); got != 24576 {
		t.Fatalf("total = %d, want 24576", got)
	}
}
