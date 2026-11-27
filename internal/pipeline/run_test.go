package pipeline_test

import (
	"os"
	"path/filepath"
	"testing"

	"media-check/internal/pipeline"
)

func TestHealthyRunProducesExpectedStructure(t *testing.T) {
	res, err := pipeline.Run(pipeline.Config{
		Seed:    17,
		WorkDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.TrackCount != 2 {
		t.Fatalf("tracks = %d, want 2", res.TrackCount)
	}
	// Video has 4 key frames (indices 0,30,60,90); audio has none.
	if res.KeyFrameCount != 4 {
		t.Fatalf("key frames = %d, want 4", res.KeyFrameCount)
	}
	if res.SyncP95Millis > 20 || res.SyncMaxMillis > 20 {
		t.Fatalf("sync error p95=%d max=%d, want <= 20 ms", res.SyncP95Millis, res.SyncMaxMillis)
	}
	if res.Stats.ProcessedUnits == 0 || res.Stats.P95LatencyMS <= 0 {
		t.Fatalf("latency stats missing: %+v", res.Stats)
	}
	if res.StructureDigest == "" || res.DamageDigest == "" {
		t.Fatal("digests must be populated")
	}
}

func TestSameSeedIsDeterministic(t *testing.T) {
	cfg := pipeline.Config{
		Seed:         17,
		LossUnits:    map[uint32][]int{1: {40, 41}},
		CorruptUnits: map[uint32][]int{1: {55}},
		WorkDir:      t.TempDir(),
	}
	r1, err := pipeline.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.WorkDir = t.TempDir()
	r2, err := pipeline.Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if r1.StructureDigest != r2.StructureDigest {
		t.Fatal("structure digest drift across identical runs")
	}
	if r1.DamageDigest != r2.DamageDigest {
		t.Fatal("damage digest drift across identical runs")
	}
}

func TestCrashThenRerunRecoveryCounters(t *testing.T) {
	dir := t.TempDir()
	// First run: artifact lands on disk but ledger commit is skipped.
	crashed, err := pipeline.Run(pipeline.Config{
		Seed: 17, WorkDir: dir, CrashAfterArtifact: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if crashed.RecoveryReport.Reused != 0 {
		t.Fatal("first crash run must not reuse anything")
	}
	// Rerun: the orphan must be adopted exactly once, never republished.
	rerun, err := pipeline.Run(pipeline.Config{
		Seed: 17, WorkDir: dir, Rerun: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rerun.RecoveryReport.Reused != 1 {
		t.Fatalf("reused = %d, want 1", rerun.RecoveryReport.Reused)
	}
	if rerun.RecoveryReport.Republished != 0 {
		t.Fatalf("republished = %d, want 0", rerun.RecoveryReport.Republished)
	}
	if rerun.RecoveryReport.MissedLedger != 1 {
		t.Fatalf("missed ledger = %d, want 1", rerun.RecoveryReport.MissedLedger)
	}
	if rerun.RecoveryReport.Quarantined != 0 {
		t.Fatalf("quarantined = %d, want 0", rerun.RecoveryReport.Quarantined)
	}
}

func TestCrashLeavesNoLedgerAndNoVersion(t *testing.T) {
	dir := t.TempDir()
	res, err := pipeline.Run(pipeline.Config{
		Seed: 17, WorkDir: dir, CrashAfterArtifact: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "publish", "ledger.json")); !os.IsNotExist(err) {
		t.Fatalf("crash must leave no ledger file, got err=%v", err)
	}
	if res.RollbackVersion != "" {
		t.Fatal("a crashed run must not produce a rollback version")
	}
}

func TestRollbackMatchesV1Manifest(t *testing.T) {
	dir := t.TempDir()
	res, err := pipeline.Run(pipeline.Config{
		Seed: 17, WorkDir: dir, RollbackTarget: "v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.RollbackVersion != "v1" {
		t.Fatalf("rollback version = %q, want v1", res.RollbackVersion)
	}
	if res.RollbackManifest == "" {
		t.Fatal("rollback manifest digest must be recorded")
	}
}

func TestDamageRecoveryRateInRange(t *testing.T) {
	res, err := pipeline.Run(pipeline.Config{
		Seed: 17, WorkDir: t.TempDir(),
		LossUnits: map[uint32][]int{1: {40, 41, 42}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Recovery.Rate < 0 || res.Recovery.Rate > 1 {
		t.Fatalf("recovery rate %f out of [0,1]", res.Recovery.Rate)
	}
	if len(res.DamageSegments) == 0 {
		t.Fatal("loss run must localize at least one damage segment")
	}
}
