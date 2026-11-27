package fault

import (
	"testing"

	"mediapipeline/internal/clock"
)

func TestReportCollectsAndFilters(t *testing.T) {
	var r Report
	zero := clock.MustRational(0, 1)
	r.Addf(CodeNALTruncated, 42, zero, zero, "cut here")
	r.Addf(CodeGap, 10, zero, zero, "lost packet")
	r.Addf(CodeGap, 20, zero, zero, "lost packet")

	if r.Count() != 3 || r.CountCode(CodeGap) != 2 {
		t.Fatalf("unexpected counts %d", r.Count())
	}
	if !r.Has(CodeNALTruncated) || r.Has(CodeChecksumMismatch) {
		t.Fatal("Has filter wrong")
	}
	f := r.All()[0]
	if f.Offset != 42 || f.Error() == "" {
		t.Fatal("fault fields not retained")
	}

	var other Report
	other.Addf(CodeBudgetExceeded, -1, zero, zero, "too big")
	r.Merge(&other)
	if r.Count() != 4 || !r.Has(CodeBudgetExceeded) {
		t.Fatal("merge failed")
	}
}
