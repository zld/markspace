package kernel

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/pipeline"
)

// TestDemoContentAddressedCache verifies the main processing path actually
// consults the content-addressed cache: the simulated interruption writes
// entries for the prefix, and the resumed run serves the still-pending
// segments that share a cache address without recomputing them as wasted work.
// No entry may be quarantined on a clean run.
func TestDemoContentAddressedCache(t *testing.T) {
	cfg := testConfig(t)
	res, err := Demo(cfg)
	if err != nil {
		t.Fatalf("Demo: %v", err)
	}
	if !res.OK {
		t.Fatalf("acceptance not met: %+v", res)
	}
	if res.Metrics.Quarantined != 0 {
		t.Fatalf("clean run quarantined cache entries: %d", res.Metrics.Quarantined)
	}
	if res.Metrics.CacheHits+res.Metrics.CacheMisses == 0 {
		t.Fatal("main pipeline never consulted the content-addressed cache")
	}
	// The prefix run stored ResumeAfter entries; the resume must find at least
	// one of them addressable (the exact hit count depends on how many pending
	// segments share the prefix's content addresses).
	if res.Metrics.CacheHits == 0 {
		t.Fatalf("expected cache hits on resume, metrics=%+v", res.Metrics)
	}
	if res.Metrics.Recomputed > 1 {
		t.Fatalf("resume recomputed finished work: %d", res.Metrics.Recomputed)
	}
}

// TestCacheQuarantineOnDigestTamper drives a real kernel cache directory and
// confirms a tampered blob is isolated and recomputed rather than served.
func TestCacheQuarantineOnDigestTamper(t *testing.T) {
	dir := t.TempDir()
	cache := &pipeline.Cache{Dir: dir}
	const input, config = "input-digest", "config-digest"
	content := []byte(`{"index":7,"sha256":"clean"}`)
	if err := cache.Put(input, config, content); err != nil {
		t.Fatal(err)
	}
	key := cache.Key(input, config)
	blob := filepath.Join(dir, key+".manifest")
	if err := os.WriteFile(blob, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	hit, err := cache.Get(input, config)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Found {
		t.Fatal("tampered cache entry was served as a hit")
	}
	if !hit.Quarantined {
		t.Fatal("tampered cache entry was not quarantined")
	}
	if cache.QuarantineCount() == 0 {
		t.Fatal("quarantine directory is empty")
	}
}
