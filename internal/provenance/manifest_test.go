package provenance

import (
	"os"
	"path/filepath"
	"testing"
)

func sample() *Manifest {
	m := New("fx1", "h264", "input-hash", "index-hash")
	m.SetStage(Stage{Name: "scan", Status: StageSucceeded})
	m.IndexHits, m.IndexMisses, m.HitRate = 9, 1, 0.9
	m.DamagedOffsets = []int64{420}
	m.SkipReasons = []string{"index_mismatch"}
	m.Segments = []SegmentRecord{
		{ID: 0, SHA256: "aaa", FirstOfHash: 0},
		{ID: 1, SHA256: "bbb", FirstOfHash: 1, Corrupt: true, Reasons: []string{"truncated_stream"}},
		{ID: 2, SHA256: "aaa", FirstOfHash: 0, Duplicate: true},
	}
	return m
}

func TestCanonicalHashStableAcrossRuns(t *testing.T) {
	first := sample()
	second := sample()
	// Wall-clock fields necessarily differ between constructions.
	if first.CreatedAt == second.CreatedAt {
		// Extremely unlikely; the test is about ignoring them, not requiring difference.
	}
	h1, err := first.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash: %v", err)
	}
	h2, err := second.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("canonical hashes differ across identical runs: %s vs %s", h1, h2)
	}
	// Full content hashes include timestamps and are allowed to differ.
	c1, _ := first.ContentHash()
	c2, _ := second.ContentHash()
	if c1 == "" || c2 == "" {
		t.Fatal("content hash empty")
	}
}

func TestPublishAtomicAndLoadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "manifest.json")
	m := sample()
	hash, err := m.Publish(path)
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if hash == "" {
		t.Fatal("publish returned empty hash")
	}
	loaded, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.FixtureID != "fx1" || loaded.InputHash != "input-hash" || len(loaded.Segments) != 3 {
		t.Fatalf("loaded manifest mismatch: %+v", loaded)
	}
	// No temp file may remain after a successful publish.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Fatal("temporary manifest file left behind")
	}
}

func TestSetStageUpsert(t *testing.T) {
	m := sample()
	m.SetStage(Stage{Name: "scan", Status: StageFailed, Detail: "boom"})
	var scan *Stage
	for i := range m.Stages {
		if m.Stages[i].Name == "scan" {
			scan = &m.Stages[i]
		}
	}
	if scan == nil || scan.Status != StageFailed || scan.Detail != "boom" || len(m.Stages) != 1 {
		t.Fatalf("stage upsert failed: %+v", m.Stages)
	}
}
