package probe

import (
	"testing"

	"github.com/broadcast/mediad/internal/model"
)

func TestSequenceAuditWraparound(t *testing.T) {
	// Build a track whose sample numbers straddle the 16-bit boundary.
	tr := &model.Track{ID: 7}
	for i := 0; i < 5; i++ {
		tr.Samples = append(tr.Samples, &model.Sample{Index: 65533 + i})
	}
	// Sample indices 65533..65537 map to 16-bit numbers 65534,65535,0,1,2 —
	// a clean wraparound with no discontinuity.
	errs := &model.ErrorSet{}
	audit := AuditSampleNumbers(tr, errs)
	if errs.Count() != 0 {
		t.Fatalf("clean wraparound must not error: %v", errs.All())
	}
	if audit.Rollovers() != 1 {
		t.Fatalf("rollover count = %d, want 1", audit.Rollovers())
	}
}

func TestSequenceAuditDetectsGap(t *testing.T) {
	tr := &model.Track{ID: 7}
	// Indices 0,1 then a jump to 4: 16-bit numbers 1,2,5 — gap at the third.
	for _, idx := range []int{0, 1, 4} {
		tr.Samples = append(tr.Samples, &model.Sample{Index: idx})
	}
	errs := &model.ErrorSet{}
	AuditSampleNumbers(tr, errs)
	if !errs.Has(model.KindSampleOutOfBounds) {
		t.Fatalf("expected discontinuity, got %v", errs.All())
	}
}
