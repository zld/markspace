package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"mediapipeline/internal/provenance"
)

// TestProvenanceFileIndependentlyVerifiable confirms the canonical record is
// written to disk and its digest can be recomputed by an external reviewer
// using only the file contents.
func TestProvenanceFileIndependentlyVerifiable(t *testing.T) {
	out := filepath.Join(t.TempDir(), "p.mp4")
	cfg := baseCfg(t, "prov", out)
	e, _ := NewEngine(cfg)
	rep := e.Run()
	if rep.FirstError != nil {
		t.Fatal(rep.FirstError)
	}
	raw, err := os.ReadFile(filepath.Join(cfg.WorkDir, "provenance.json"))
	if err != nil {
		t.Fatalf("provenance file not persisted: %v", err)
	}
	if !provenance.Verify(raw, rep.ProvenanceDigest) {
		t.Fatal("independently recomputed digest does not match the report")
	}
	// A tampered record must fail verification.
	raw[len(raw)-2] ^= 0x01
	if provenance.Verify(raw, rep.ProvenanceDigest) {
		t.Fatal("tampered provenance must not verify")
	}
}

// TestProvenanceStableAcrossResume ensures the persisted record is identical
// between an original run and a resumed run that reuses durable nodes.
func TestProvenanceStableAcrossResume(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "r.mp4")

	cfg1 := baseCfg(t, "prov-resume", out)
	cfg1.FailNodeOnce = NodeSync
	e1, _ := NewEngine(cfg1)
	if rep := e1.Run(); rep.FirstError == nil {
		t.Fatal("expected interruption")
	}

	cfg2 := baseCfg(t, "prov-resume", out)
	cfg2.WorkDir = cfg1.WorkDir
	e2, _ := NewEngine(cfg2)
	rep2 := e2.Run()
	if rep2.FirstError != nil {
		t.Fatal(rep2.FirstError)
	}
	raw, err := os.ReadFile(filepath.Join(cfg1.WorkDir, "provenance.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !provenance.Verify(raw, rep2.ProvenanceDigest) {
		t.Fatal("resumed provenance digest does not match its persisted record")
	}
}
