package pipeline

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"replay/internal/cache"
	"replay/internal/clock"
	"replay/internal/events"
	"replay/internal/testkit"
)

func baseConfig(t *testing.T) (*Config, string) {
	t.Helper()
	dir := t.TempDir()
	if err := testkit.Generate(filepath.Join(dir, "fx")); err != nil {
		t.Fatalf("fixtures: %v", err)
	}
	cfg := &Config{
		Input:         filepath.Join(dir, "fx", "clean.mp4"),
		Events:        filepath.Join(dir, "fx", "events.jsonl"),
		OutDir:        filepath.Join(dir, "out"),
		CacheDir:      filepath.Join(dir, "cache"),
		RTPClock:      90000,
		RTPAnchor:     events.Anchor{RtpTick: testkit.AnchorTick, MediaTick: 0},
		Pre:           time.Second,
		Post:          2 * time.Second,
		MinConfidence: 0.6,
		Budget:        5 * time.Second,
	}
	return cfg, dir
}

func TestFullRunAndStableDigest(t *testing.T) {
	cfg, _ := baseConfig(t)
	st, mf, err := Run(cfg)
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	if st.Accepted != 1 || st.Duplicate != 1 || st.SkipBudget != 1 || st.SkipConfidence != 1 {
		t.Fatalf("unexpected stats: %+v", st)
	}
	first := mf.SHA

	st2, mf2, err := Run(cfg)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if st2.CacheHits != 1 {
		t.Fatalf("cache_hits=%d want 1", st2.CacheHits)
	}
	if mf2.SHA != first {
		t.Fatalf("manifest digest changed across runs:\n%s\n%s", first, mf2.SHA)
	}
}

func TestStopAfterCutResumesWithoutDuplicates(t *testing.T) {
	cfg, _ := baseConfig(t)
	cfg.StopAfter = StageCut
	_, _, err := Run(cfg)
	se, ok := err.(*StopAfter)
	if !ok || se.Stage() != StageCut {
		t.Fatalf("want StopAfter(cut), got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(cfg.OutDir, "manifest.json")); !os.IsNotExist(statErr) {
		t.Fatal("manifest must not be committed at cut stage")
	}

	cfg.StopAfter = ""
	st, mf, err := Run(cfg)
	if err != nil {
		t.Fatalf("resume run: %v", err)
	}
	seen := map[string]bool{}
	for _, e := range mf.Events {
		if e.Clip != nil {
			if seen[e.ID] {
				t.Fatalf("duplicate clip owner for event %s", e.ID)
			}
			seen[e.ID] = true
		}
	}
	if st.Accepted != 1 {
		t.Fatalf("accepted after resume=%d want 1", st.Accepted)
	}
}

func TestTamperedCacheTriggersRebuild(t *testing.T) {
	cfg, _ := baseConfig(t)
	if _, _, err := Run(cfg); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	entries, err := os.ReadDir(cfg.CacheDir)
	if err != nil {
		t.Fatal(err)
	}
	var bin string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".bin" {
			bin = filepath.Join(cfg.CacheDir, e.Name())
		}
	}
	if bin == "" {
		t.Fatal("no cached artifact found")
	}
	if err := os.WriteFile(bin, []byte("tampered-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _, err := Run(cfg)
	if err != nil {
		t.Fatalf("run after tamper: %v", err)
	}
	if st.CacheRebuilds != 1 {
		t.Fatalf("cache_rebuilds=%d want 1", st.CacheRebuilds)
	}
}

func TestCheckpointBindingRejectsChangedInput(t *testing.T) {
	cfg, dir := baseConfig(t)
	if _, _, err := Run(cfg); err != nil {
		t.Fatalf("seed run: %v", err)
	}
	store, err := NewStore(filepath.Join(cfg.OutDir, ".state"))
	if err != nil {
		t.Fatal(err)
	}
	cp, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if err := cp.Matches("deadbeef", cp.EventsSHA, cp.ConfigSHA, cp.InputSize); err == nil {
		t.Fatal("expected digest mismatch error")
	} else if ce, ok := clock.AsClockError(err); !ok || ce.Kind != clock.KindCheckpoint {
		t.Fatalf("want checkpoint error, got %v", err)
	}
	_ = dir
}

func TestCacheKeyBindsSourceAndConfig(t *testing.T) {
	sum := [32]byte{}
	k1 := cache.Key(sum, "cfg-A")
	k2 := cache.Key(sum, "cfg-B")
	if k1 == k2 {
		t.Fatal("different config must produce different cache keys")
	}
	var other [32]byte
	other[0] = 1
	if cache.Key(other, "cfg-A") == k1 {
		t.Fatal("different source must produce different cache keys")
	}
}
