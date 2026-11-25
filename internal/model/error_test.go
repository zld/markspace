package model

import "testing"

func TestIssueLog(t *testing.T) {
	var log IssueLog
	log.Add(Issue{Code: CodeBoundaryDrift, Severity: SeverityError, Layer: LayerSegment, ByteOffset: 42})
	log.Add(Issue{Code: CodeOverlappingCue, Severity: SeverityWarning, Layer: LayerSubtitle, ByteOffset: 7})
	log.Add(Issue{Code: CodeBoundaryDrift, Severity: SeverityError, Layer: LayerSegment, ByteOffset: 99, Representation: "mid"})

	if !log.HasError() {
		t.Error("expected error-severity issue")
	}
	counts := log.CountByCode()
	if counts[CodeBoundaryDrift] != 2 || counts[CodeOverlappingCue] != 1 {
		t.Errorf("counts=%v", counts)
	}
	mid := log.Filter("mid")
	if len(mid) != 1 || mid[0].ByteOffset != 99 {
		t.Errorf("filter mid=%v", mid)
	}
	if got := (log.All()[0]).String(); got == "" {
		t.Error("issue string must be non-empty")
	}
}

func TestIssueStringCarriesFields(t *testing.T) {
	i := Issue{Code: CodeWraparound, Severity: SeverityError, Layer: LayerClock, ByteOffset: 128, Track: "video"}
	s := i.String()
	for _, want := range []string{"wraparound", "clock", "offset=128", "track=video"} {
		if !contains(s, want) {
			t.Errorf("String()=%q missing %q", s, want)
		}
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
