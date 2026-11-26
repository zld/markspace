package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestSelftestAllProofCounters runs the exact selftest scenario graph against
// a temp directory and locks the documented acceptance counters and the
// expected negative-case exit codes.
func TestSelftestAllProofCounters(t *testing.T) {
	out := t.TempDir()
	p, ok, err := executeAll(out, false)
	if err != nil {
		t.Fatalf("executeAll: %v", err)
	}
	if !ok {
		for _, c := range p.Cases {
			if !c.OK {
				t.Errorf("case %s failed: %s", c.Name, c.Line)
			}
		}
		t.Fatal("expected all selftest cases to meet expectations")
	}

	if p.Roundtrip != "100%" {
		t.Errorf("roundtrip=%s want 100%%", p.Roundtrip)
	}
	if p.IndexHits != "100%" {
		t.Errorf("index_hits=%s want 100%%", p.IndexHits)
	}
	if p.BoundarySkewTicks != 0 {
		t.Errorf("boundary_skew_ticks=%d want 0", p.BoundarySkewTicks)
	}
	if p.Segments != 6 {
		t.Errorf("segments=%d want 6", p.Segments)
	}
	if p.HashMatch != "6/6" {
		t.Errorf("hash_match=%s want 6/6", p.HashMatch)
	}
	if p.Anomalies != 0 {
		t.Errorf("normal anomalies=%d want 0", p.Anomalies)
	}
	if p.Lost != 1 {
		t.Errorf("lost=%d want 1", p.Lost)
	}
	if p.Duplicate != 1 {
		t.Errorf("duplicate=%d want 1", p.Duplicate)
	}
	if p.Truncated != 1 {
		t.Errorf("truncated=%d want 1", p.Truncated)
	}
	if p.IndexRebuilt != 1 {
		t.Errorf("index_rebuilt=%d want 1", p.IndexRebuilt)
	}
	if p.Rebuilt != 1 {
		t.Errorf("rebuilt=%d want 1", p.Rebuilt)
	}
	if p.Reused != 5 {
		t.Errorf("reused=%d want 5", p.Reused)
	}
	if p.DuplicateProducts != 0 {
		t.Errorf("duplicate_products=%d want 0", p.DuplicateProducts)
	}
	if p.ResumeHashMatch != "6/6" {
		t.Errorf("resume hash=%s want 6/6", p.ResumeHashMatch)
	}
	if len(p.Cases) != len(caseOrder) {
		t.Fatalf("cases=%d want %d", len(p.Cases), len(caseOrder))
	}
	if _, err := os.Stat(filepath.Join(out, "proof.json")); err != nil {
		t.Fatalf("proof.json not retained: %v", err)
	}
}

// TestNegativeCaseExitCodes confirms the production status mapping: an
// unrecoverable interval expects 2, fatal/verify failures expect 1.
func TestNegativeCaseExitCodes(t *testing.T) {
	cases := map[string]int{
		"unrecoverable": 2,
		"fatal":         1,
		"verifyfail":    1,
		"normal":        0,
	}
	for name, want := range cases {
		fn := caseFuncs()[name]
		r := fn(t.TempDir())
		if !r.OK {
			t.Errorf("%s did not meet its own expectation: %s", name, r.Line)
		}
		if r.ExpectedCode != want {
			t.Errorf("%s expected code=%d got %d", name, want, r.ExpectedCode)
		}
	}
}
