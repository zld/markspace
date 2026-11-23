package model

import (
	"errors"
	"testing"
)

func TestParseFaultKind(t *testing.T) {
	for _, k := range []FaultKind{FaultNone, FaultLate, FaultTruncate, FaultMetaConflict} {
		got, err := ParseFaultKind(string(k))
		if err != nil || got != k {
			t.Errorf("ParseFaultKind(%q) = %q,%v", k, got, err)
		}
	}
	if _, err := ParseFaultKind("nope"); err == nil {
		t.Fatal("unknown fault must error")
	}
}

func TestRangeOverlaps(t *testing.T) {
	if !(Range{0, 10}).Overlaps(Range{5, 15}) {
		t.Fatal("overlapping ranges not detected")
	}
	if (Range{0, 10}).Overlaps(Range{10, 20}) {
		t.Fatal("half-open boundary must not overlap")
	}
	if (Range{10, 5}).Valid() {
		t.Fatal("inverted range must be invalid")
	}
}

func TestStageError(t *testing.T) {
	e := NewStageError(StageCommit, ErrOverflow)
	if e.Stage != StageCommit || e.Error() == "" || !errors.Is(e, ErrOverflow) {
		t.Fatalf("stage error wrong: %v", e)
	}
}
