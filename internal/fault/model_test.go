package fault

import "testing"

func TestLogCollectsAndFilters(t *testing.T) {
	log := NewLog()
	log.Add(Record{
		Stage: StageManifest, Severity: SeverityError,
		Kind: KindMissingSegment, Segment: "seg3.m4s", MediaSeq: 3,
		SourceOffset: 412, Message: "gap in sequence",
	})
	log.Add(Record{
		Stage: StageParse, Severity: SeverityWarning,
		Kind: KindDiscontinuity, Segment: "seg2.m4s", MediaSeq: 2,
	})

	if !log.HasError() || log.ErrorCount() != 1 {
		t.Fatalf("expected exactly one error, got %d", log.ErrorCount())
	}
	if got := len(log.ByStage(StageManifest)); got != 1 {
		t.Fatalf("manifest records = %d, want 1", got)
	}
	recs := log.Records()
	if recs[0].SafeBoundary != -1 {
		t.Fatalf("default safe boundary = %d, want -1", recs[0].SafeBoundary)
	}
	if recs[0].String() == "" {
		t.Fatal("record string should be non-empty")
	}

	// Returned slice must be a copy: mutating it must not alter the log.
	recs[0] = Record{}
	if log.Records()[0].Kind != KindMissingSegment {
		t.Fatal("Records() must return a defensive copy")
	}
}

func TestAddUniqueDeduplicates(t *testing.T) {
	log := NewLog()
	base := Record{
		Stage: StageManifest, Severity: SeverityError,
		Kind: KindMissingSegment, Segment: "s2.m4s", MediaSeq: 2,
		SourceOffset: 40, Message: "first",
	}
	if !log.AddUnique(base) {
		t.Fatal("first AddUnique must insert")
	}
	if log.AddUnique(base) {
		t.Fatal("identical AddUnique must not re-insert")
	}
	if log.AddUnique(Record{
		Stage: StageManifest, Severity: SeverityError,
		Kind: KindMissingSegment, Segment: "s2.m4s", MediaSeq: 2,
		SourceOffset: 99, Message: "different offset",
	}) != true {
		t.Fatal("a different source offset is a distinct record")
	}
	if len(log.Records()) != 2 {
		t.Fatalf("record count = %d, want 2", len(log.Records()))
	}
}
