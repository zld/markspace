package scan

import (
	"path/filepath"
	"testing"

	"mediapipeline/internal/fault"
	"mediapipeline/internal/fixture"
)

// scanAll runs ReadFragment over every fragment file in input-index order.
func scanAll(t *testing.T, descs []fixture.RepresentationDescriptor, repIdx int) []*Fragment {
	t.Helper()
	rep := descs[repIdx]
	names := fragmentNamesForTest(t, rep.Directory)
	out := make([]*Fragment, 0, len(names))
	for i, name := range names {
		f, err := ReadFragment(i, rep.ID, filepath.Join(rep.Directory, name))
		if err != nil {
			t.Fatalf("unexpected scan error on %s: %v", name, err)
		}
		out = append(out, f)
	}
	return out
}

func TestSequenceNormal(t *testing.T) {
	descs := generate(t, fixture.CaseNormal, 1, 5)
	tr := NewSequenceTracker(descs[0].ID)
	for _, f := range scanAll(t, descs, 0) {
		if err := tr.Observe(f); err != nil {
			t.Fatal(err)
		}
	}
	rep := tr.Report()
	if len(rep.Fragments) != 5 || len(rep.Gaps) != 0 || len(rep.Regressions) != 0 {
		t.Fatalf("healthy report wrong: %s", rep)
	}
}

func TestSequenceGap(t *testing.T) {
	descs := generate(t, fixture.CaseMfhdGap, 1, 5)
	tr := NewSequenceTracker(descs[0].ID)
	for _, f := range scanAll(t, descs, 0) {
		if err := tr.Observe(f); err != nil {
			t.Fatal(err)
		}
	}
	rep := tr.Report()
	if len(rep.Gaps) != 1 || rep.Gaps[0].MissingMFHD != fixture.GapSeqForTest() {
		t.Fatalf("gap not recorded: %+v", rep.Gaps)
	}
	if len(rep.Fragments) != 4 {
		t.Fatalf("accepted fragments = %d, want 4", len(rep.Fragments))
	}
}

func TestTFDTRegressionRecordedAndIsolated(t *testing.T) {
	descs := generate(t, fixture.CaseTFDTRegression, 1, 5)
	tr := NewSequenceTracker(descs[0].ID)
	for _, f := range scanAll(t, descs, 0) {
		if err := tr.Observe(f); err != nil {
			t.Fatal(err)
		}
	}
	rep := tr.Report()
	if len(rep.Regressions) != 1 {
		t.Fatalf("regression count = %d, want 1", len(rep.Regressions))
	}
	reg := rep.Regressions[0]
	if reg.MFHD != fixture.RegressionSeqForTest() || reg.ObservedTFDT >= reg.PreviousTFDT {
		t.Fatalf("regression values wrong: %+v", reg)
	}
	if reg.InputSHA256 == "" || reg.InputPath == "" {
		t.Fatal("regression must retain input digest and path")
	}
	if len(rep.Isolated) != 1 {
		t.Fatalf("isolated count = %d, want 1", len(rep.Isolated))
	}
	for _, f := range rep.Fragments {
		if f.MFHD == reg.MFHD {
			t.Fatal("regressed fragment must not stay in the accepted set")
		}
	}
}

func TestDuplicateFoldAndConflict(t *testing.T) {
	t.Run("fold", func(t *testing.T) {
		descs := generate(t, fixture.CaseDuplicateFold, 1, 5)
		tr := NewSequenceTracker(descs[0].ID)
		for _, f := range scanAll(t, descs, 0) {
			if err := tr.Observe(f); err != nil {
				t.Fatalf("identical duplicate should fold: %v", err)
			}
		}
		if len(tr.Report().Fragments) != 5 {
			t.Fatalf("folded report should keep 5 fragments, got %d", len(tr.Report().Fragments))
		}
	})
	t.Run("conflict", func(t *testing.T) {
		descs := generate(t, fixture.CaseDuplicateConflict, 1, 5)
		tr := NewSequenceTracker(descs[0].ID)
		var conflict error
		for _, f := range scanAll(t, descs, 0) {
			if err := tr.Observe(f); err != nil {
				conflict = err
				break
			}
		}
		if !fault.Is(conflict, fault.CodeDuplicateConflict) {
			t.Fatalf("expected duplicate_conflict, got %v", conflict)
		}
		fe := conflict.(*fault.Error)
		if fe.Detail["first_sha256"] == fe.Detail["duplicate_sha256"] {
			t.Fatal("conflict detail must expose differing digests")
		}
		if fe.Detail["first_tfdt"] == fe.Detail["duplicate_tfdt"] {
			t.Fatal("conflict detail must expose differing tfdt values")
		}
	})
}
