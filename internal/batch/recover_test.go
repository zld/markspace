package batch_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediapack/internal/batch"
	"mediapack/internal/demo"
	"mediapack/internal/metrics"
)

func TestBatchRecoveryIsDeterministicAndVerifiable(t *testing.T) {
	dir := t.TempDir()
	cfg, _, err := demo.Generate(dir, 8)
	if err != nil {
		t.Fatal(err)
	}
	first, err := batch.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if first.Manifest.SampleCount == 0 || first.Manifest.PayloadHashMismatches != 0 {
		t.Fatalf("bad first manifest: %+v", first.Manifest)
	}

	lost, err := batch.SimulateLostLastCheckpoint(dir)
	if err != nil || lost == 0 {
		t.Fatalf("lost checkpoint=%d err=%v", lost, err)
	}
	recovered, err := batch.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Manifest.RecoveredChunks != 1 {
		t.Fatalf("recovered=%d", recovered.Manifest.RecoveredChunks)
	}
	if recovered.Manifest.SampleCount != first.Manifest.SampleCount {
		t.Fatal("sample count changed after recovery")
	}
	if cp := recovered.Manifest.CommittedChunks; cp != first.Manifest.CommittedChunks {
		t.Fatalf("committed chunks=%d want %d", cp, first.Manifest.CommittedChunks)
	}

	manifestPath := filepath.Join(dir, "manifest.json")
	hash1, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := batch.Run(cfg); err != nil {
		t.Fatal(err)
	}
	hash2, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(hash1) != string(hash2) {
		t.Fatal("manifest changed on an idempotent rerun")
	}
	report, _, err := metrics.VerifyDirectory(dir)
	if err != nil {
		t.Fatalf("verify: %v report=%+v", err, report)
	}
}
