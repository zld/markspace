package recover

import (
	"testing"

	auditerrors "media-audit/internal/errors"
)

func TestMergeRangesRemovesDuplicates(t *testing.T) {
	got := MergeRanges([]Range{
		{Start: 10, End: 20}, {Start: 10, End: 20},
		{Start: 0, End: 10}, {Start: 15, End: 30},
	})
	want := Range{Start: 0, End: 30}
	if len(got) != 1 || got[0] != want {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestJournalDoesNotDuplicateSuccessfulRange(t *testing.T) {
	j := &Journal{}
	j.Add(Range{Start: 100, End: 140})
	j.Add(Range{Start: 100, End: 140})
	j.AddMany([]Range{{Start: 120, End: 160}, {Start: 200, End: 220}})
	got := j.Ranges()
	if len(got) != 2 {
		t.Fatalf("ranges = %+v", got)
	}
	if got[0] != (Range{Start: 100, End: 160}) || got[1] != (Range{Start: 200, End: 220}) {
		t.Fatalf("unexpected merged ranges: %+v", got)
	}
}

func TestPlanOnlyIncludesDamageAndDependencies(t *testing.T) {
	e := auditerrors.New(auditerrors.CodeChecksumMismatch, 130, "bad").
		WithBytes(120, 180)
	plans := PlanForError(e,
		[]Range{{Start: 100, End: 120}},
		[]Range{{Start: 0, End: 100}, {Start: 100, End: 120}, {Start: 120, End: 180}, {Start: 180, End: 300}},
	)
	if len(plans.Recompute) != 1 || plans.Recompute[0] != (Range{Start: 100, End: 180}) {
		t.Fatalf("recompute = %+v", plans.Recompute)
	}
	if len(plans.Reuse) != 2 ||
		plans.Reuse[0] != (Range{Start: 0, End: 100}) ||
		plans.Reuse[1] != (Range{Start: 180, End: 300}) {
		t.Fatalf("reuse = %+v", plans.Reuse)
	}
}
