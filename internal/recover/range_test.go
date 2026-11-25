package recover

import (
	"testing"

	auditerrors "media-audit/internal/errors"
)

func TestMergeRangesRemovesDuplicates(t *testing.T) {
	got := MergeRanges([]Range{{10, 20}, {10, 20}, {0, 10}, {15, 30}})
	want := []Range{{0, 30}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestJournalDoesNotDuplicateSuccessfulRange(t *testing.T) {
	j := &Journal{}
	j.Add(Range{100, 140})
	j.Add(Range{100, 140})
	j.AddMany([]Range{{120, 160}, {200, 220}})
	got := j.Ranges()
	if len(got) != 2 {
		t.Fatalf("ranges = %+v", got)
	}
	if got[0] != (Range{100, 160}) || got[1] != (Range{200, 220}) {
		t.Fatalf("unexpected merged ranges: %+v", got)
	}
}

func TestPlanOnlyIncludesDamageAndDependencies(t *testing.T) {
	e := auditerrors.New(auditerrors.CodeChecksumMismatch, 130, "bad").
		WithBytes(120, 180)
	plans := PlanForError(e, []Range{{100, 120}}, []Range{{0, 100}, {100, 120}, {120, 180}, {180, 300}})
	if len(plans.Recompute) != 1 || plans.Recompute[0] != (Range{100, 180}) {
		t.Fatalf("recompute = %+v", plans.Recompute)
	}
	if len(plans.Reuse) != 2 || plans.Reuse[0] != (Range{0, 100}) || plans.Reuse[1] != (Range{180, 300}) {
		t.Fatalf("reuse = %+v", plans.Reuse)
	}
}
