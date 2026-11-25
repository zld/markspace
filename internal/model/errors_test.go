package model

import (
	"strings"
	"testing"
)

func TestReportCountsAndFatals(t *testing.T) {
	r := &Report{}
	if r.HasFatal() {
		t.Fatal("empty report must not be fatal")
	}
	r.Addf(CodeMFHDGap, SevWarn, "seg_3.m4s", -1, "gap of %d", 2)
	r.Addf(CodeMFHDGap, SevWarn, "seg_7.m4s", -1, "gap of %d", 1)
	r.Addf(CodeTruncatedBox, SevFatal, "seg_9.m4s", 128, "cut off")
	r.Addf(CodeColorMissing, SevInfo, "", -1, "no colr box")

	if got := r.Count(CodeMFHDGap); got != 2 {
		t.Fatalf("Count(MFHD_GAP)=%d, want 2", got)
	}
	if !r.HasFatal() {
		t.Fatal("expected fatal issue")
	}
	if len(r.Fatals()) != 1 || r.Fatals()[0].Code != CodeTruncatedBox {
		t.Fatalf("unexpected fatals: %+v", r.Fatals())
	}
	counts := r.SortedCounts()
	if len(counts) != 3 {
		t.Fatalf("expected 3 codes, got %d", len(counts))
	}
	for i := 1; i < len(counts); i++ {
		if counts[i-1].Code >= counts[i].Code {
			t.Fatalf("counts not sorted: %+v", counts)
		}
	}
}

func TestIssueString(t *testing.T) {
	i := Issue{Code: CodeTruncatedBox, Severity: SevWarn, File: "a.m4s", Offset: 42, Message: "cut"}
	s := i.String()
	if !strings.Contains(s, "a.m4s@42") || !strings.Contains(s, "TRUNCATED_BOX") {
		t.Fatalf("unexpected format: %q", s)
	}
	j := Issue{Code: CodeColorMissing, Severity: SevInfo, Offset: -1, Message: "x"}
	if !strings.Contains(j.String(), "(-)") {
		t.Fatalf("expected placeholder location: %q", j.String())
	}
}

func TestSeverityString(t *testing.T) {
	if SevInfo.String() != "info" || SevWarn.String() != "warn" || SevFatal.String() != "fatal" {
		t.Fatal("severity names broken")
	}
}
