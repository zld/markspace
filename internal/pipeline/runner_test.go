package pipeline

import (
	"bytes"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.CheckpointDir = filepath.Join(dir, "checkpoints")
	cfg.CacheDir = filepath.Join(dir, "cache")
	return cfg
}

func run(t *testing.T, cfg Config) *Summary {
	t.Helper()
	r, err := New(cfg)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	s, err := r.Run()
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return s
}

func TestRunnerMeetsAcceptance(t *testing.T) {
	s := run(t, testConfig(t))
	if s.Segments != 6 {
		t.Fatalf("segments %d", s.Segments)
	}
	if s.RoundtripMatchRate < 0.99 {
		t.Fatalf("roundtrip %.4f", s.RoundtripMatchRate)
	}
	if s.KeyframeIndexHitRate < 0.98 {
		t.Fatalf("keyframe %.4f", s.KeyframeIndexHitRate)
	}
	if len(s.SegmentHashes) != 6 {
		t.Fatalf("hashes %d", len(s.SegmentHashes))
	}
	for i, h := range s.SegmentHashes {
		if len(h) != 64 {
			t.Fatalf("segment %d hash %q not 64 hex chars", i, h)
		}
		if _, err := hex.DecodeString(h); err != nil {
			t.Fatalf("segment %d hash not hex: %v", i, err)
		}
	}
	if s.ReorderedPackets == 0 {
		t.Fatal("reordering fault did not occur")
	}
	if s.UnrecoveredGaps == 0 {
		t.Fatal("unrecovered gap expected from late retransmissions")
	}
	if s.RecoveredPackets == 0 {
		t.Fatal("recovery did not occur")
	}
	if !s.OK() {
		t.Fatalf("acceptance not met: %+v", s)
	}
}

func TestRunnerDeterministicHashes(t *testing.T) {
	cfg := testConfig(t)
	first := run(t, cfg)
	cfg2 := testConfig(t)
	second := run(t, cfg2)
	if len(first.SegmentHashes) != len(second.SegmentHashes) {
		t.Fatalf("hash count %d vs %d", len(first.SegmentHashes), len(second.SegmentHashes))
	}
	for i := range first.SegmentHashes {
		if first.SegmentHashes[i] != second.SegmentHashes[i] {
			t.Fatalf("segment %d hash differs across runs: %s vs %s",
				i+1, first.SegmentHashes[i], second.SegmentHashes[i])
		}
	}
	if first.ReorderedPackets != second.ReorderedPackets || first.RecoveredPackets != second.RecoveredPackets {
		t.Fatalf("counters differ: %+v vs %+v", first, second)
	}
}

func TestRunnerCacheHitOnSecondRun(t *testing.T) {
	cfg := testConfig(t)
	first := run(t, cfg)
	if first.CacheHits != 0 {
		t.Fatalf("cold run reported %d hits", first.CacheHits)
	}
	second := run(t, cfg)
	if second.CacheHits != first.Segments {
		t.Fatalf("warm run hits %d, want %d", second.CacheHits, first.Segments)
	}
	if second.CacheRejected != 0 {
		t.Fatalf("warm run rejected %d", second.CacheRejected)
	}
	for i := range first.SegmentHashes {
		if first.SegmentHashes[i] != second.SegmentHashes[i] {
			t.Fatalf("cached segment %d hash changed", i+1)
		}
	}
}

func TestRunnerInterruptAndResume(t *testing.T) {
	cfg := testConfig(t)
	cfg.InterruptAfter = 3
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(); !errors.Is(err, ErrInterrupted) {
		t.Fatalf("expected interruption, got %v", err)
	}
	resume := cfg
	resume.InterruptAfter = 0
	resume.Resume = true
	r2, err := New(resume)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r2.Run()
	if err != nil {
		t.Fatal(err)
	}
	if s.CheckpointResumes != 1 {
		t.Fatalf("resumes %d", s.CheckpointResumes)
	}
	if s.CheckpointCommits != 3 {
		t.Fatalf("resumed commits %d, want remaining 3", s.CheckpointCommits)
	}
	if s.Segments != 6 {
		t.Fatalf("resumed segments %d", s.Segments)
	}
	if s.CacheHits < 3 {
		t.Fatalf("resume did not reuse committed cache entries: %d hits", s.CacheHits)
	}
	// The resumed output must be identical to a single uninterrupted run.
	full := run(t, testConfig(t))
	if len(full.SegmentHashes) != len(s.SegmentHashes) {
		t.Fatalf("hash count %d vs %d", len(full.SegmentHashes), len(s.SegmentHashes))
	}
	for i := range full.SegmentHashes {
		if full.SegmentHashes[i] != s.SegmentHashes[i] {
			t.Fatalf("segment %d hash differs after resume", i+1)
		}
	}
}

func TestRunnerResumeRejectsChangedInput(t *testing.T) {
	cfg := testConfig(t)
	cfg.InterruptAfter = 2
	r, _ := New(cfg)
	if _, err := r.Run(); !errors.Is(err, ErrInterrupted) {
		t.Fatal("expected interruption")
	}
	changed := cfg
	changed.Seed = 99
	changed.Resume = true
	r2, err := New(changed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r2.Run(); !errors.Is(err, ErrResumeMismatch) {
		t.Fatalf("resume with changed input accepted: %v", err)
	}
}

func TestRunnerConfigValidation(t *testing.T) {
	cases := []func(*Config){
		func(c *Config) { c.Seed = -1 },
		func(c *Config) { c.JitterMS = 900 },
		func(c *Config) { c.LossBurst = 99 },
		func(c *Config) { c.Window = 0 },
		func(c *Config) { c.HoldTicks = -1 },
		func(c *Config) { c.RecoverAttempts = 0 },
		func(c *Config) { c.CheckpointDir = "" },
		func(c *Config) { c.CacheDir = "" },
		func(c *Config) { c.InterruptAfter = 99 },
		func(c *Config) { c.LateRatio = 2000 },
	}
	for i, mutate := range cases {
		cfg := testConfig(t)
		mutate(&cfg)
		if _, err := New(cfg); err == nil {
			t.Fatalf("case %d accepted invalid config", i)
		}
	}
}

func TestRunnerCorruptCacheIsRejectedAndRecomputed(t *testing.T) {
	cfg := testConfig(t)
	first := run(t, cfg)
	if first.CacheHits != 0 {
		t.Fatalf("cold hits %d", first.CacheHits)
	}
	// Corrupt one payload before the warm run. The store must quarantine it and
	// recompute, and the recomputed hash must still match the cold run.
	bins, err := filepath.Glob(filepath.Join(cfg.CacheDir, "*", "*.bin"))
	if err != nil || len(bins) == 0 {
		t.Fatalf("no cache payloads: %v", err)
	}
	if err := os.WriteFile(bins[0], []byte("forged"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := run(t, cfg)
	if second.CacheRejected == 0 {
		t.Fatalf("corrupt entry not rejected")
	}
	if second.CacheHits != first.Segments-1 {
		t.Fatalf("hits %d rejected %d", second.CacheHits, second.CacheRejected)
	}
	found := false
	for i := range first.SegmentHashes {
		if first.SegmentHashes[i] != second.SegmentHashes[i] {
			t.Fatalf("segment %d hash changed after recompute", i+1)
		}
		if bytes.Equal([]byte(first.SegmentHashes[i]), []byte(second.SegmentHashes[i])) {
			found = true
		}
	}
	if !found {
		t.Fatal("no hashes compared")
	}
}

func TestRunnerPropagatesMissingStateAcrossSubsystems(t *testing.T) {
	cfg := testConfig(t)
	cfg.LateRatio = 1000 // every loss is unrecoverable
	cfg.LossBurst = 4
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	s, err := r.Run()
	if err != nil {
		t.Fatal(err)
	}
	if s.UnrecoveredGaps == 0 {
		t.Fatal("expected unrecovered gaps")
	}
	if s.MissingGroups == 0 {
		t.Fatal("missing groups not propagated to segment results")
	}
	if s.RoundtripMatchRate < 0.99 {
		t.Fatalf("arrived media should still match: %.4f", s.RoundtripMatchRate)
	}
	// The missing state must reach sync: at least one segment must record an
	// unaligned result caused by the transport hole.
	sawUnresolved := false
	for _, res := range r.results {
		if !res.Sync.Aligned && res.Sync.VideoSamples > 0 {
			sawUnresolved = true
		}
		for _, g := range res.Transport.MissingGroups {
			if g < 0 {
				t.Fatalf("bad group index")
			}
		}
	}
	if !sawUnresolved && s.MissingGroups > 0 {
		t.Fatal("missing state did not affect alignment")
	}
}
