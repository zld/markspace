package model

import "testing"

func TestRecorderSeverity(t *testing.T) {
	r := NewRecorder()
	r.Add(ErrorRecord{Code: ErrMetadataConflict, Severity: SevWarn, Track: "video", Message: "mild"})
	if r.HasError() {
		t.Fatal("warn record must not set error flag")
	}
	r.Add(ErrorRecord{Code: ErrTruncatedHeader, Severity: SevError, Offset: 128, Message: "cut"})
	if !r.HasError() {
		t.Fatal("error record must set error flag")
	}
	if len(r.Records()) != 2 {
		t.Fatalf("records = %d, want 2", len(r.Records()))
	}
	if r.CountByCode()[ErrTruncatedHeader] != 1 {
		t.Fatalf("count by code wrong: %v", r.CountByCode())
	}
}

func TestRecordDefaultsAndString(t *testing.T) {
	r := NewRecorder()
	r.Add(ErrorRecord{Code: ErrCorruptPacket, Track: "audio", Offset: 42})
	rec := r.Records()[0]
	if rec.Severity != SevError {
		t.Fatalf("default severity = %q, want error", rec.Severity)
	}
	s := rec.String()
	for _, want := range []string{"CORRUPT_PACKET", "track=audio", "offset=42"} {
		if !contains(s, want) {
			t.Errorf("String() = %q, missing %q", s, want)
		}
	}
}

func TestJoinAndReset(t *testing.T) {
	r := NewRecorder()
	r.Add(ErrorRecord{Code: ErrMissingPacket, Severity: SevWarn, Message: "gap"})
	r.Add(ErrorRecord{Code: ErrAlignmentExceeded, Severity: SevError, Message: "drift"})
	if got := r.JoinMessages(); !contains(got, "drift") || contains(got, "gap") {
		t.Fatalf("JoinMessages = %q, want only error entries", got)
	}
	r.Reset()
	if r.HasError() || len(r.Records()) != 0 {
		t.Fatal("reset did not clear recorder")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
