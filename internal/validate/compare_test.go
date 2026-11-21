package validate

import (
	"strings"
	"testing"

	"streamcheck/internal/manifest"
)

func TestCompareRates(t *testing.T) {
	base := []manifest.Segment{seg(0, 800, 4000), seg(1, 800, 4000), seg(2, 800, 4000)}
	same := []manifest.Segment{seg(0, 1600, 4001), seg(1, 1600, 4001), seg(2, 1600, 3999)}
	lower := AnalyzeRate(800, base, nil, false)
	high := AnalyzeRate(1600, same, nil, false)
	checks := []RateCheck{lower, high}
	CompareRates(checks, 80)
	if len(checks[1].Issues) != 0 {
		t.Fatalf("unexpected issues: %v", checks[1].Issues)
	}
	if checks[1].DurationDeltaMS != 2 {
		t.Fatalf("delta = %d, want 2", checks[1].DurationDeltaMS)
	}

	drift := []manifest.Segment{seg(0, 1600, 4000), seg(1, 1600, 4000), seg(2, 1600, 4200)}
	driftChecks := []RateCheck{lower, AnalyzeRate(1600, drift, nil, false)}
	CompareRates(driftChecks, 80)
	if len(driftChecks[1].BoundaryIssues) != 1 || driftChecks[1].BoundaryIssues[0].SegmentID != 2 {
		t.Fatalf("boundary issues = %+v", driftChecks[1].BoundaryIssues)
	}
	if driftChecks[1].DurationDeltaMS != 200 {
		t.Fatalf("delta = %d, want 200", driftChecks[1].DurationDeltaMS)
	}
	if len(driftChecks[1].Issues) != 1 {
		t.Fatalf("drift issues = %v", driftChecks[1].Issues)
	}

	short := []manifest.Segment{seg(0, 1600, 4000), seg(1, 1600, 4000)}
	missing := AnalyzeRate(1600, short, nil, false)
	shortChecks := []RateCheck{lower, missing}
	CompareRates(shortChecks, 80)
	joined := strings.Join(shortChecks[1].Issues, "\n")
	if !strings.Contains(joined, "segment_count 2") || !strings.Contains(joined, "segment_ids [0-1] differ") {
		t.Fatalf("issues = %s", joined)
	}
}
