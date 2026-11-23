package ingest_test

import (
	"testing"

	"mediacore/internal/fault"
	"mediacore/internal/fixture"
	"mediacore/internal/ingest"
)

func scanStream(t *testing.T, data []byte) (*ingest.InitInfo, *ingest.Index, *ingest.ScanResult, *ingest.Reader) {
	t.Helper()
	r := newReader(t, data)
	info, f := ingest.ParseInit(r)
	if f != nil {
		t.Fatalf("init: %v", f)
	}
	idx, _ := ingest.ScanIndex(r, info.MoovOffset+info.MoovSize)
	res := r.ScanFragmentsBounded(info.MoovOffset+info.MoovSize, idx)
	return info, idx, res, r
}

func TestScanIndexCompleteStream(t *testing.T) {
	stream, err := fixture.Build(fixture.DefaultSpec(41, 2, 24))
	if err != nil {
		t.Fatal(err)
	}
	_, idx, res, _ := scanStream(t, stream.Data)
	if idx == nil || !idx.Complete {
		t.Fatalf("index not complete: %+v", idx)
	}
	if idx.DeclaredReferences != 24 || len(idx.References) != 24 {
		t.Fatalf("references=%d declared=%d", len(idx.References), idx.DeclaredReferences)
	}
	if len(res.Fragments) != 24 {
		t.Fatalf("fragments=%d", len(res.Fragments))
	}
	if res.IndexGap || res.Stopped || len(res.Errors) != 0 {
		t.Fatalf("unexpected scan state: %+v errors=%v", res, res.Errors)
	}
}

func TestScanRecoversFromShortIndex(t *testing.T) {
	injected, err := fixture.Inject(41, 2, 24, []fixture.FaultKind{fixture.FaultIndexGap})
	if err != nil {
		t.Fatal(err)
	}
	_, idx, res, _ := scanStream(t, injected.Data)
	if idx == nil {
		t.Fatal("index missing")
	}
	if !idx.Complete {
		t.Fatal("a deliberately shortened index is still self-consistent")
	}
	if len(idx.References) != 12 {
		t.Fatalf("index references=%d", len(idx.References))
	}
	if res.IndexCovered != 12 {
		t.Fatalf("index coverage=%d", res.IndexCovered)
	}
	if len(res.Fragments) != 24 {
		t.Fatalf("bounded scan recovered %d fragments, want 24", len(res.Fragments))
	}
	if !res.IndexGap {
		t.Fatal("index gap not flagged")
	}
	if len(res.Errors) == 0 || res.Errors[0].Code != fault.CodeIndexGap {
		t.Fatalf("missing index_gap error: %v", res.Errors)
	}
	if res.Errors[0].Offset != injected.Injections[0].ByteOffset {
		t.Fatalf("index gap offset=%d want %d", res.Errors[0].Offset, injected.Injections[0].ByteOffset)
	}
	want := injected.Fragments[12].MoofOffset
	if res.Errors[0].Offset != want {
		t.Fatalf("index gap offset=%d want first uncovered moof %d", res.Errors[0].Offset, want)
	}
}

func TestCorruptMdatIsNotResynchronised(t *testing.T) {
	injected, err := fixture.Inject(41, 2, 24, []fixture.FaultKind{fixture.FaultCorruptMdat})
	if err != nil {
		t.Fatal(err)
	}
	_, _, res, _ := scanStream(t, injected.Data)
	if !res.Stopped {
		t.Fatal("scan must stop at a corrupt mdat instead of resynchronising")
	}
	for _, f := range res.Fragments {
		if f.MdatOffset == injected.Injections[0].ByteOffset {
			t.Fatal("forged moof inside mdat was accepted as a fragment")
		}
	}
	var found bool
	for _, e := range res.Errors {
		if e.Code == fault.CodeSampleOutOfRange {
			found = true
		}
	}
	if !found {
		t.Fatalf("sample range violation not reported: %v", res.Errors)
	}
}

func TestScanStopsAtBoxLimitAndResumes(t *testing.T) {
	stream, err := fixture.Build(fixture.DefaultSpec(41, 2, 48))
	if err != nil {
		t.Fatal(err)
	}
	r, f := ingest.NewReader("scan.fmp4", stream.Data, ingest.Limits{MaxBoxes: 1 << 16, MaxScanBoxes: 10})
	if f != nil {
		t.Fatal(f)
	}
	info, f := ingest.ParseInit(r)
	if f != nil {
		t.Fatal(f)
	}
	res := r.ScanFragmentsBounded(info.MoovOffset+info.MoovSize, nil)
	if !res.Stopped {
		t.Fatal("scan limit not enforced")
	}
	if len(res.Errors) == 0 || res.Errors[len(res.Errors)-1].Code != fault.CodeScanLimit {
		t.Fatalf("missing scan_limit fault: %v", res.Errors)
	}
	if res.ResumeAt <= 0 || res.ResumeAt >= int64(len(stream.Data)) {
		t.Fatalf("resume offset %d is not a valid continuation point", res.ResumeAt)
	}
	// Resuming from the recorded offset must make progress without redoing
	// already scanned boxes.
	res2 := r.ScanFragmentsBounded(res.ResumeAt, nil)
	if len(res2.Fragments) == 0 {
		t.Fatal("resume produced no fragments")
	}
	if res2.Fragments[0].MoofOffset != res.ResumeAt && res2.Fragments[0].MoofOffset < res.ResumeAt {
		t.Fatalf("resume replayed boxes before %d", res.ResumeAt)
	}
}

func TestScanEnforcesFragmentDurationLimit(t *testing.T) {
	stream, err := fixture.Build(fixture.DefaultSpec(41, 2, 4))
	if err != nil {
		t.Fatal(err)
	}
	r, f := ingest.NewReader("dur.fmp4", stream.Data, ingest.Limits{MaxFileBytes: 1 << 30, MaxScanBoxes: 1000, MaxFragmentDuration: 5000})
	if f != nil {
		t.Fatal(f)
	}
	info, f := ingest.ParseInit(r)
	if f != nil {
		t.Fatal(f)
	}
	res := r.ScanFragmentsBounded(info.MoovOffset+info.MoovSize, nil)
	if len(res.Fragments) != 0 || !res.Stopped {
		t.Fatalf("duration limit not enforced: %+v", res)
	}
	if len(res.Errors) == 0 || res.Errors[0].Code != fault.CodeDurationLimit {
		t.Fatalf("expected duration_limit, got %v", res.Errors)
	}
	if res.ResumeAt != res.Errors[0].Offset {
		t.Fatalf("resume %d != fault offset %d", res.ResumeAt, res.Errors[0].Offset)
	}
}
