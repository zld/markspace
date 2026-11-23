package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

// TestRunnerCacheHitSkipsWork populates the content-addressed store on a first
// run, then starts a fresh runner (no checkpoint) backed by the same cache and
// proves every pending segment is served from the cache without invoking the
// work function. A cache hit still commits a digest-verified checkpoint entry.
func TestRunnerCacheHitSkipsWork(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "checkpoint.json")
	cache := &Cache{Dir: filepath.Join(dir, "cache")}
	cfg := RunConfig{TotalSegments: 6, Cache: cache}

	workCalls := map[int]int{}
	first, err := NewRunner(cpPath, "input", "config", RunConfig{TotalSegments: 6})
	if err != nil {
		t.Fatal(err)
	}
	res, err := first.Run(cfg, func(i int) ([]byte, string, error) {
		workCalls[i]++
		content := []byte{byte(i), 0xAB, 0xCD}
		return content, DigestBytes(content), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheMisses != 6 || res.CacheHits != 0 || res.Recomputed != 6 {
		t.Fatalf("first run = %+v", res)
	}
	if len(workCalls) != 6 {
		t.Fatalf("work invoked %d times, want 6", len(workCalls))
	}

	// Fresh process simulation: remove the checkpoint but keep the cache.
	if err := os.Remove(cpPath); err != nil {
		t.Fatal(err)
	}
	second, err := NewRunner(cpPath, "input", "config", RunConfig{TotalSegments: 6})
	if err != nil {
		t.Fatal(err)
	}
	callsAgain := map[int]int{}
	res2, err := second.Run(cfg, func(i int) ([]byte, string, error) {
		callsAgain[i]++
		return []byte{byte(i), 0xAB, 0xCD}, DigestBytes([]byte{byte(i), 0xAB, 0xCD}), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(callsAgain) != 0 {
		t.Fatalf("work recomputed despite cache: %v", callsAgain)
	}
	if res2.CacheHits != 6 || res2.CacheMisses != 0 || res2.Completed != 6 {
		t.Fatalf("second run = %+v", res2)
	}
	// The checkpoint rebuilt from cache content must carry identical digests.
	cp, err := LoadCheckpoint(cpPath, "input", "config")
	if err != nil {
		t.Fatal(err)
	}
	if len(cp.Completed) != 6 {
		t.Fatalf("checkpoint completed = %d", len(cp.Completed))
	}
}

// TestRunnerCacheTamperQuarantinesAndRecomputes tampers one cached blob so the
// store quarantines it; the runner must recompute that segment from work and
// still finish, never serving the tampered bytes.
func TestRunnerCacheTamperQuarantinesAndRecomputes(t *testing.T) {
	dir := t.TempDir()
	cpPath := filepath.Join(dir, "checkpoint.json")
	cache := &Cache{Dir: filepath.Join(dir, "cache")}
	first, err := NewRunner(cpPath, "input", "config", RunConfig{TotalSegments: 4})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Run(RunConfig{TotalSegments: 4, Cache: cache}, func(i int) ([]byte, string, error) {
		content := []byte{byte(i), 1, 2, 3}
		return content, DigestBytes(content), nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(cpPath); err != nil {
		t.Fatal(err)
	}

	// Corrupt every stored blob's bytes so the content-hash check fails.
	blobs, err := os.ReadDir(cache.Dir)
	if err != nil {
		t.Fatal(err)
	}
	tampered := 0
	for _, f := range blobs {
		p := filepath.Join(cache.Dir, f.Name())
		if filepath.Ext(p) != ".manifest" {
			continue
		}
		if err := os.WriteFile(p, []byte("tampered-bytes"), 0o644); err != nil {
			t.Fatal(err)
		}
		tampered++
	}
	if tampered == 0 {
		t.Fatal("no cached blobs found")
	}

	second, err := NewRunner(cpPath, "input", "config", RunConfig{TotalSegments: 4})
	if err != nil {
		t.Fatal(err)
	}
	recomputed := map[int]int{}
	res, err := second.Run(RunConfig{TotalSegments: 4, Cache: cache}, func(i int) ([]byte, string, error) {
		recomputed[i]++
		content := []byte{byte(i), 1, 2, 3}
		return content, DigestBytes(content), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheHits != 0 {
		t.Fatalf("tampered cache produced hits: %+v", res)
	}
	if res.Quarantined != 4 || res.CacheMisses != 4 || res.Recomputed != 4 {
		t.Fatalf("run = %+v", res)
	}
	if len(recomputed) != 4 {
		t.Fatalf("recomputed %v, want all four", recomputed)
	}
	if cache.QuarantineCount() == 0 {
		t.Fatal("quarantine directory empty after tamper detection")
	}
}

// TestRunnerCacheKeyedByConfig confirms a different config digest never hits
// entries stored for the original config.
func TestRunnerCacheKeyedByConfig(t *testing.T) {
	dir := t.TempDir()
	cache := &Cache{Dir: filepath.Join(dir, "cache")}
	first, err := NewRunner(filepath.Join(dir, "cp1.json"), "input", "configA", RunConfig{TotalSegments: 3})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Run(RunConfig{TotalSegments: 3, Cache: cache}, func(i int) ([]byte, string, error) {
		content := []byte{byte(i)}
		return content, DigestBytes(content), nil
	}); err != nil {
		t.Fatal(err)
	}
	second, err := NewRunner(filepath.Join(dir, "cp2.json"), "input", "configB", RunConfig{TotalSegments: 3})
	if err != nil {
		t.Fatal(err)
	}
	calls := map[int]int{}
	res, err := second.Run(RunConfig{TotalSegments: 3, Cache: cache}, func(i int) ([]byte, string, error) {
		calls[i]++
		content := []byte{byte(i)}
		return content, DigestBytes(content), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.CacheHits != 0 || len(calls) != 3 {
		t.Fatalf("config mismatch leaked a cache hit: %+v calls=%v", res, calls)
	}
}
