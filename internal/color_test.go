package medpipe

import "testing"

func TestColorFirstKnownWinsNoConflict(t *testing.T) {
	v := NewColorValidator("v")
	v.Observe(FrameMeta{ColorPrimaries: PrimariesBT709, Transfer: TransferSRGB, Matrix: MatrixBT709}, 10)
	v.Observe(FrameMeta{ColorPrimaries: PrimariesBT709, Transfer: TransferSRGB, Matrix: MatrixBT709}, 40)
	rep, errs := v.Finalize()
	if !rep.HasSignal || len(rep.Conflicts) != 0 || len(rep.Unknown) != 0 || len(errs) != 0 {
		t.Fatalf("consistent metadata reported issues: %+v %+v", rep, errs)
	}
}

func TestColorConflictCountedWithSourceAndAdopted(t *testing.T) {
	v := NewColorValidator("v")
	v.Observe(FrameMeta{ColorPrimaries: PrimariesBT709, Transfer: TransferSRGB, Matrix: MatrixBT709}, 10)
	// Later frame signals BT.2020 primaries: conflict, first value retained.
	v.Observe(FrameMeta{ColorPrimaries: PrimariesBT2020, Transfer: TransferSRGB, Matrix: MatrixBT709}, 80)
	v.Observe(FrameMeta{ColorPrimaries: PrimariesBT2020, Transfer: TransferPQ, Matrix: MatrixBT2020NCL}, 120)
	rep, errs := v.Finalize()
	// Offset 80 contributes one conflict; offset 120 conflicts on all three
	// fields because the adopted triple stays BT.709/sRGB end to end.
	if rep.ConflictCount() != 4 {
		t.Fatalf("want 4 conflicts, got %d (%+v)", rep.ConflictCount(), rep.Conflicts)
	}
	if rep.Adopted.ColorPrimaries != PrimariesBT709 {
		t.Fatal("adopted primaries must stay first-known BT.709")
	}
	c0 := rep.Conflicts[0]
	if c0.SourceVal != PrimariesBT2020 || c0.AdoptedVal != PrimariesBT709 || c0.Offset != 80 {
		t.Fatalf("conflict record wrong: %+v", c0)
	}
	if len(errs) != 4 || errs[0].Code != CodeColorConflict || errs[0].Kind != KindRecoverable {
		t.Fatalf("conflicts must be recoverable errors, got %+v", errs)
	}
}

func TestColorUnknownDoesNotOverride(t *testing.T) {
	v := NewColorValidator("v")
	// Unknown first, then a known value: the known value must be adopted.
	v.Observe(FrameMeta{ColorPrimaries: CICPUnspecified, Transfer: 99, Matrix: 0}, 10)
	v.Observe(FrameMeta{ColorPrimaries: PrimariesBT709, Transfer: TransferSRGB, Matrix: MatrixBT709}, 40)
	rep, errs := v.Finalize()
	if rep.Adopted.ColorPrimaries != PrimariesBT709 || rep.Adopted.Transfer != TransferSRGB {
		t.Fatalf("unknown value overrode adopted metadata: %+v", rep.Adopted)
	}
	if len(rep.Unknown) != 2 {
		t.Fatalf("want 2 unknown records, got %d", len(rep.Unknown))
	}
	if len(errs) != 2 || errs[0].Code != CodeColorUnknown {
		t.Fatalf("unknown values must be recoverable errors, got %+v", errs)
	}
}

func TestColorNoSignalIsReportedNotFailed(t *testing.T) {
	v := NewColorValidator("v")
	v.Observe(FrameMeta{}, 0)
	rep, errs := v.Finalize()
	if rep.HasSignal {
		t.Fatal("track with no color metadata must not claim a signal")
	}
	if len(errs) != 0 {
		t.Fatalf("missing color metadata must not fail the track, got %+v", errs)
	}
}
