package pipeline

import (
	"fmt"

	"mediaqc/internal/clock"
	"mediaqc/internal/model"
)

// Invariants validates the cross-stage state rules from the specification:
//   - output timestamps are monotonic,
//   - every output fragment traces to a source event,
//   - recovered packets only fill declared gaps,
//   - an error status is never cleared by a later successful stage.
type Invariants struct {
	Failures []string
}

// NewInvariants creates an empty checker.
func NewInvariants() *Invariants { return &Invariants{} }

func (iv *Invariants) fail(format string, args ...any) {
	iv.Failures = append(iv.Failures, fmt.Sprintf(format, args...))
}

// Check runs all invariant rules for one material.
func (iv *Invariants) Check(materialID string, sourceSeqs map[uint64]bool,
	output []model.Event, gaps []model.Gap, hadError bool, finalOK bool) {
	iv.checkMonotonic(materialID, output)
	iv.checkProvenance(materialID, sourceSeqs, output)
	iv.checkRecoveryGaps(materialID, output, gaps)
	iv.checkErrorSticky(materialID, hadError, finalOK)
}

func (iv *Invariants) checkMonotonic(materialID string, output []model.Event) {
	for i := 1; i < len(output); i++ {
		if clock.Cmp(output[i-1].Start, output[i].Start) > 0 {
			iv.fail("%s: output timestamp non-monotonic between seq %d and %d",
				materialID, output[i-1].Sequence, output[i].Sequence)
		}
	}
}

func (iv *Invariants) checkProvenance(materialID string, sourceSeqs map[uint64]bool, output []model.Event) {
	for _, ev := range output {
		if ev.Recovered {
			continue // recovered events are validated against gaps separately
		}
		if !sourceSeqs[ev.Sequence] {
			iv.fail("%s: output fragment seq %d has no originating source event",
				materialID, ev.Sequence)
		}
	}
}

func (iv *Invariants) checkRecoveryGaps(materialID string, output []model.Event, gaps []model.Gap) {
	declared := map[uint64]bool{}
	for _, g := range gaps {
		for s := g.FirstSeq; s <= g.LastSeq; s++ {
			declared[s] = true
		}
	}
	for _, ev := range output {
		if !ev.Recovered {
			continue
		}
		if !ev.HasGapRef {
			iv.fail("%s: recovered seq %d carries no gap reference", materialID, ev.Sequence)
			continue
		}
		if ev.FillsGapSeq != ev.Sequence {
			iv.fail("%s: recovered seq %d references different gap seq %d",
				materialID, ev.Sequence, ev.FillsGapSeq)
		}
		if !declared[ev.FillsGapSeq] {
			iv.fail("%s: recovered packet fills undeclared gap seq %d",
				materialID, ev.FillsGapSeq)
		}
	}
}

func (iv *Invariants) checkErrorSticky(materialID string, hadError, finalOK bool) {
	if hadError && finalOK {
		iv.fail("%s: error status was cleared by a later successful stage", materialID)
	}
}

// Failed reports whether any invariant was violated.
func (iv *Invariants) Failed() bool { return len(iv.Failures) > 0 }
