package journal_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"medfix/internal/journal"
)

func writeStage(t *testing.T, s journal.CheckpointStore, runID, stage string, idx int, inputSHA, cfgHash, prev string) {
	t.Helper()
	if err := s.Write(&journal.Checkpoint{
		RunID: runID, Stage: stage, StageIndex: idx,
		InputSHA256: inputSHA, ConfigHash: cfgHash, PrevHash: prev,
	}); err != nil {
		t.Fatalf("write %s: %v", stage, err)
	}
}

func TestCheckpointWriteReadAndHashChain(t *testing.T) {
	dir := t.TempDir()
	s, err := journal.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	writeStage(t, s, "r", journal.StageParse, 0, "in", "cfg", "")
	cp, err := s.Read(journal.StageParse)
	if err != nil {
		t.Fatal(err)
	}
	if cp.BodyHash == "" {
		t.Fatal("body hash not populated")
	}
	writeStage(t, s, "r", journal.StageTransform, 1, "in", "cfg", cp.BodyHash)
	latest, idx, err := s.LatestValid("in", "cfg")
	if err != nil {
		t.Fatal(err)
	}
	if latest.Stage != journal.StageTransform || idx != 1 {
		t.Fatalf("latest=%s idx=%d", latest.Stage, idx)
	}
}

func TestCheckpointMissingSentinel(t *testing.T) {
	s, _ := journal.NewStore(t.TempDir())
	if _, err := s.Read(journal.StageCommit); !errors.Is(err, journal.ErrCheckpointMissing) {
		t.Fatalf("want ErrCheckpointMissing, got %v", err)
	}
}

func TestCorruptCheckpointRollback(t *testing.T) {
	dir := t.TempDir()
	s, _ := journal.NewStore(dir)
	writeStage(t, s, "r", journal.StageParse, 0, "in", "cfg", "")
	cp, _ := s.Read(journal.StageParse)
	writeStage(t, s, "r", journal.StageTransform, 1, "in", "cfg", cp.BodyHash)

	// Corrupt the transform checkpoint on disk.
	path := filepath.Join(dir, "checkpoint-transform.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	latest, idx, err := s.LatestValid("in", "cfg")
	if err != nil {
		t.Fatalf("rollback should return last valid, got %v", err)
	}
	if latest.Stage != journal.StageParse || idx != 0 {
		t.Fatalf("did not roll back to parse: %s %d", latest.Stage, idx)
	}
	if _, err := s.Read(journal.StageTransform); !errors.Is(err, journal.ErrCheckpointMissing) {
		t.Fatal("corrupt and later checkpoints must be removed")
	}
}

func TestFingerprintAndConfigInvalidation(t *testing.T) {
	// Fingerprint mismatch uses an isolated store.
	dirFP := t.TempDir()
	sFP, _ := journal.NewStore(dirFP)
	writeStage(t, sFP, "r", journal.StageParse, 0, "in1", "cfg1", "")
	if _, _, err := sFP.LatestValid("in2", "cfg1"); !errors.Is(err, journal.ErrFingerprintMismatch) {
		t.Fatalf("want fingerprint mismatch, got %v", err)
	}

	// Config change uses a fresh store; all old checkpoints invalidated.
	dirCfg := t.TempDir()
	sCfg, _ := journal.NewStore(dirCfg)
	writeStage(t, sCfg, "r", journal.StageParse, 0, "in1", "cfg1", "")
	if _, _, err := sCfg.LatestValid("in1", "cfg2"); !errors.Is(err, journal.ErrConfigMismatch) {
		t.Fatalf("want config mismatch, got %v", err)
	}
	if _, err := sCfg.Read(journal.StageParse); !errors.Is(err, journal.ErrCheckpointMissing) {
		t.Fatal("config change must wipe old checkpoints")
	}
}

func TestProvenanceSegmentHashStableAndVerified(t *testing.T) {
	hashes := []string{"a", "b", "c"}
	h1 := journal.SegmentHash(hashes)
	h2 := journal.SegmentHash(hashes)
	if h1 != h2 {
		t.Fatal("segment hash unstable for identical input")
	}
	if journal.SegmentHash([]string{"a", "b", "d"}) == h1 {
		t.Fatal("different content must yield different hash")
	}
	log := journal.ProvenanceLog{RunID: "r"}
	rec := journal.ProvenanceRecord{
		SegmentID: 0, SegmentSHA256: h1, SourceID: "src",
		InputOffsets: []int64{10, 20}, RuleIDs: []string{"rule-x"},
	}
	if err := log.Add(rec); err != nil {
		t.Fatal(err)
	}
	if err := log.Add(rec); err == nil {
		t.Fatal("duplicate segment id must be rejected")
	}
	path := filepath.Join(t.TempDir(), "prov.json")
	if err := log.Save(path); err != nil {
		t.Fatal(err)
	}
	loaded, err := journal.LoadProvenance(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := loaded.Lookup(0); !ok || got.SegmentSHA256 != h1 {
		t.Fatal("provenance lookup failed")
	}
	// Out-of-range offset and unknown rule produce verification errors.
	bad := journal.ProvenanceLog{RunID: "r", Records: []journal.ProvenanceRecord{{
		SegmentID: 1, InputOffsets: []int64{999}, RuleIDs: []string{"ghost"},
	}}}
	if errs := bad.Verify(100, map[string]bool{"rule-x": true}); len(errs) != 2 {
		t.Fatalf("want 2 verification errors, got %d", len(errs))
	}
}
