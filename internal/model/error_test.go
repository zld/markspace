package model

import "testing"

func TestFindingSortIsStable(t *testing.T) {
	seqA, seqB := uint32(9), uint32(4)
	in := Findings{
		{Code: ErrMissingIndex, Source: SourceMP4, Span: Span{Start: 50, End: 60}},
		{Code: ErrDuplicatePacket, Source: SourceRTP, Span: Span{Start: 10, End: 20}, Sequence: &seqA},
		{Code: ErrSequenceGap, Source: SourceRTP, Span: Span{Start: 10, End: 20}, Sequence: &seqB},
	}
	SortFindings(in)
	if in[0].Code != ErrSequenceGap || in[1].Code != ErrDuplicatePacket {
		t.Fatalf("sort order wrong: %+v", in)
	}
	if in[2].Code != ErrMissingIndex {
		t.Fatalf("third finding = %+v", in[2])
	}
}

func TestFindingCounts(t *testing.T) {
	seq := uint32(1)
	in := Findings{
		{Code: ErrSequenceGap, Severity: SeverityWarning, Sequence: &seq},
		{Code: ErrSequenceGap, Severity: SeverityError},
		{Code: ErrTruncatedPacket, Severity: SeverityError},
	}
	if got := CountCode(in, ErrSequenceGap); got != 2 {
		t.Fatalf("CountCode = %d", got)
	}
	if got := CountSeverity(in, SeverityError); got != 2 {
		t.Fatalf("CountSeverity = %d", got)
	}
	if got := MaxSeverity(in); got != SeverityError {
		t.Fatalf("MaxSeverity = %s", got)
	}
	if got := MaxSeverity(nil); got != SeverityInfo {
		t.Fatalf("empty MaxSeverity = %s", got)
	}
}

func TestNewFindingFormatting(t *testing.T) {
	f := NewFinding(ErrUnknownPayload, SourceCodec, SeverityWarning, Span{Start: 5, End: 9}, "payload %d unknown", 42)
	if f.Message != "payload 42 unknown" || f.Span.Start != 5 || f.Span.End != 9 {
		t.Fatalf("unexpected finding: %+v", f)
	}
}
