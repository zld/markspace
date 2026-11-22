package pipeline

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediapipe/internal/cache"
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
	if second.SegmentCacheHits != first.Segments {
		t.Fatalf("warm run segment hits %d, want %d", second.SegmentCacheHits, first.Segments)
	}
	if second.CacheHits <= second.SegmentCacheHits {
		t.Fatalf("warm run served no stage entries: %d total, %d segment",
			second.CacheHits, second.SegmentCacheHits)
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
	if s.SegmentCacheHits < 3 {
		t.Fatalf("resume did not reuse committed cache entries: %d segment hits", s.SegmentCacheHits)
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
	// Corrupt the committed index entry of one segment before the warm run. The
	// store must quarantine it and recompute, and the recomputed hash must
	// still match the cold run.
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	records, err := r.journal.Records()
	if err != nil || len(records) == 0 {
		t.Fatalf("no checkpoint records: %v", err)
	}
	committed := records[0].Committed[0].Digest
	target := filepath.Join(cfg.CacheDir, string(committed[:2]), string(committed)+".bin")
	if err := os.WriteFile(target, []byte("forged"), 0o644); err != nil {
		t.Fatal(err)
	}
	second := run(t, cfg)
	if second.CacheRejected == 0 {
		t.Fatalf("corrupt entry not rejected")
	}
	if second.SegmentCacheHits != first.Segments-1 {
		t.Fatalf("segment hits %d rejected %d", second.SegmentCacheHits, second.CacheRejected)
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

func TestCheckpointRecordsSequenceWindowsAndFragments(t *testing.T) {
	cfg := testConfig(t)
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(); err != nil {
		t.Fatal(err)
	}
	records, err := r.journal.Records()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 {
		t.Fatalf("records %d", len(records))
	}
	for i, rec := range records {
		if rec.SeqWindow.Count == 0 {
			t.Fatalf("record %d has empty sequence window", i)
		}
		// Start may be numerically greater than End when the window crosses the
		// 16-bit wrap. Count is the number of packets that actually reached the
		// emission point, so it never exceeds the inclusive span and is smaller
		// exactly when a hole was declared inside the window.
		span := int(uint16(rec.SeqWindow.End-rec.SeqWindow.Start)) + 1
		if rec.SeqWindow.Count > span {
			t.Fatalf("record %d window count %d exceeds span %d", i, rec.SeqWindow.Count, span)
		}
		if len(rec.Committed) != 1 || rec.Committed[0].Sequence != uint32(i+1) {
			t.Fatalf("record %d committed %+v", i, rec.Committed)
		}
		// Output is the semantic stage digest; the committed digest is the
		// content address of the cached result bytes. Both must be valid and
		// the cached bytes must hash to the committed digest.
		if !rec.Output.Valid() || !rec.Committed[0].Digest.Valid() {
			t.Fatalf("record %d invalid digest", i)
		}
		store, err := cache.Open(cfg.CacheDir)
		if err != nil {
			t.Fatal(err)
		}
		got, err := store.Fetch(cache.Lookup{Key: rec.Committed[0].Digest, Stage: StageIndex,
			Version: Version, Input: r.source()})
		if err != nil {
			t.Fatalf("record %d cache fetch: %v", i, err)
		}
		if cache.KeyFromBytes(got.Bytes) != rec.Committed[0].Digest {
			t.Fatalf("record %d cached bytes do not match committed digest", i)
		}
		var res SegmentResult
		if err := json.Unmarshal(got.Bytes, &res); err != nil {
			t.Fatal(err)
		}
		if res.Digest != rec.Output {
			t.Fatalf("record %d output digest differs from cached result digest", i)
		}
		if rec.Input != r.InputKey() {
			t.Fatalf("record %d input digest mismatch", i)
		}
	}
}

func TestRunnerStageCachesAreReused(t *testing.T) {
	cfg := testConfig(t)
	if s := run(t, cfg); s.CacheHits != 0 {
		t.Fatalf("cold run hits %d", s.CacheHits)
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(); err != nil {
		t.Fatal(err)
	}
	// Three stage entries per segment must be served warm: transport,
	// container and sync, independently of the final index entry.
	if got, want := r.stageHits(), 3*6; got != want {
		t.Fatalf("stage hits %d, want %d", got, want)
	}
	if r.indexHits != 6 {
		t.Fatalf("index hits %d", r.indexHits)
	}
	if r.rejects != 0 {
		t.Fatalf("unexpected rejections %d", r.rejects)
	}
}

func TestRunnerStageCacheDetectsInputDrift(t *testing.T) {
	cfg := testConfig(t)
	run(t, cfg)
	// A different window changes the scenario input key: no stage entry may be
	// served, because every stage key is derived from that address.
	changed := cfg
	changed.Window = 32
	s := run(t, changed)
	if s.CacheHits != 0 {
		t.Fatalf("changed input reused %d entries", s.CacheHits)
	}
}

func TestRunnerStageCacheVersionMismatchRejected(t *testing.T) {
	cfg := testConfig(t)
	first := run(t, cfg)
	if first.Segments != 6 {
		t.Fatal("setup failed")
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Rewriting one stage manifest with a foreign version must trigger
	// quarantine plus recompute rather than a silent hit.
	records, err := r.journal.Records()
	if err != nil || len(records) == 0 {
		t.Fatal("no records")
	}
	digest := records[0].Committed[0].Digest
	man := filepath.Join(cfg.CacheDir, string(digest[:2]), string(digest)+".manifest.json")
	raw, err := os.ReadFile(man)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	m["version"] = "mediapipe/0"
	patched, _ := json.Marshal(m)
	if err := os.WriteFile(man, patched, 0o644); err != nil {
		t.Fatal(err)
	}
	resumed := cfg
	resumed.Resume = true
	s := run(t, resumed)
	if s.CacheRejected == 0 {
		t.Fatalf("foreign-version entry not rejected")
	}
	if s.Segments != 6 {
		t.Fatalf("segments %d", s.Segments)
	}
}
