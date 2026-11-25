package media

import "testing"

func TestQualityFatalCategories(t *testing.T) {
	fatal := []Category{
		CatTruncatedHeader, CatChunkOutOfBounds, CatPartialFrame, CatBadContainer,
		CatRTPReorder, CatRTPClockUnavailable, CatRTPMalformed,
		CatIntervalGap, CatIntervalOverlap, CatWorkerFailed,
	}
	for _, c := range fatal {
		if !QualityFatal(c) {
			t.Errorf("category %s must fail the quality gate", c)
		}
	}
	nonFatal := []Category{CatRTPSeqWrap, CatRTPTSWrap, CatCanceled}
	for _, c := range nonFatal {
		if QualityFatal(c) {
			t.Errorf("category %s must not fail the quality gate", c)
		}
	}
}

func TestFaultAnchors(t *testing.T) {
	bf := NewByteFault(CatChunkOutOfBounds, 36, 44, "chunk %d exceeds buffer", 2)
	if bf.ByteStart != 36 || bf.ByteEnd != 44 || bf.SampleStart != -1 {
		t.Fatalf("byte fault anchors wrong: %+v", bf)
	}
	if bf.Message != "chunk 2 exceeds buffer" {
		t.Fatalf("fault message = %q", bf.Message)
	}
	sf := NewSampleFault(CatIntervalGap, 100, 120, "gap")
	if sf.SampleStart != 100 || sf.SampleEnd != 120 || sf.ByteStart != -1 {
		t.Fatalf("sample fault anchors wrong: %+v", sf)
	}
}

func TestHasFatal(t *testing.T) {
	faults := []Fault{
		NewFault(CatRTPTSWrap, "wrapped"),
		NewSampleFault(CatIntervalGap, 0, 10, "gap"),
	}
	if !HasFatal(faults) {
		t.Fatal("HasFatal must detect the interval gap")
	}
	onlyInfo := []Fault{NewFault(CatRTPSeqWrap, "wrap")}
	if HasFatal(onlyInfo) {
		t.Fatal("wrap-only fault list must pass the quality gate")
	}
	if got := FatalCategories(faults); len(got) != 1 || got[0].Category != CatIntervalGap {
		t.Fatalf("FatalCategories = %+v, want only the gap", got)
	}
}
