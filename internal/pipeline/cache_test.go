package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func newCache(t *testing.T) Cache {
	t.Helper()
	return Cache{Dir: filepath.Join(t.TempDir(), "cache")}
}

func TestCacheMissThenHit(t *testing.T) {
	c := newCache(t)
	hit, err := c.Get("inputA", "configA")
	if err != nil {
		t.Fatal(err)
	}
	if hit.Found || hit.Quarantined {
		t.Fatalf("empty cache hit = %+v", hit)
	}
	content := []byte(`{"segments":[0,1,2]}`)
	if err := c.Put("inputA", "configA", content); err != nil {
		t.Fatal(err)
	}
	hit, err = c.Get("inputA", "configA")
	if err != nil {
		t.Fatal(err)
	}
	if !hit.Found || string(hit.Content) != string(content) {
		t.Fatalf("hit = %+v", hit)
	}
}

func TestCacheTamperedContentQuarantined(t *testing.T) {
	c := newCache(t)
	_ = c.Put("inputA", "configA", []byte("good"))
	key := c.Key("inputA", "configA")
	if err := os.WriteFile(c.contentPath(key), []byte("evil"), 0o644); err != nil {
		t.Fatal(err)
	}
	hit, err := c.Get("inputA", "configA")
	if err != nil {
		t.Fatal(err)
	}
	if hit.Found || !hit.Quarantined {
		t.Fatalf("tampered hit = %+v", hit)
	}
	if c.QuarantineCount() == 0 {
		t.Fatal("tampered entry was not quarantined")
	}
	// A fresh put must now succeed and be served.
	if err := c.Put("inputA", "configA", []byte("good again")); err != nil {
		t.Fatal(err)
	}
	hit, err = c.Get("inputA", "configA")
	if err != nil || !hit.Found || string(hit.Content) != "good again" {
		t.Fatalf("post-repair hit = %+v err %v", hit, err)
	}
}

func TestCacheConfigMismatchQuarantined(t *testing.T) {
	c := newCache(t)
	_ = c.Put("inputA", "configA", []byte("payload"))
	key := c.Key("inputA", "configA")
	// Rewrite metadata so it claims a different config.
	entry := `{"input_digest":"inputA","config_digest":"configB","content_hash":"deadbeef"}`
	if err := os.WriteFile(c.entryPath(key), []byte(entry), 0o644); err != nil {
		t.Fatal(err)
	}
	hit, err := c.Get("inputA", "configA")
	if err != nil {
		t.Fatal(err)
	}
	if hit.Found || !hit.Quarantined {
		t.Fatalf("config mismatch hit = %+v", hit)
	}
}

func TestCacheInputMismatchQuarantined(t *testing.T) {
	c := newCache(t)
	_ = c.Put("inputA", "configA", []byte("payload"))
	hit, err := c.Get("inputB", "configA")
	if err != nil {
		t.Fatal(err)
	}
	// Different input yields a different key, so this is a clean miss, not a
	// quarantine: entries are addressed by input+config together.
	if hit.Found || hit.Quarantined {
		t.Fatalf("different input hit = %+v", hit)
	}
	if c.QuarantineCount() != 0 {
		t.Fatal("unrelated key quarantined")
	}
}

func TestCacheCorruptMetadataQuarantined(t *testing.T) {
	c := newCache(t)
	_ = c.Put("i", "c", []byte("payload"))
	key := c.Key("i", "c")
	if err := os.WriteFile(c.entryPath(key), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	hit, err := c.Get("i", "c")
	if err != nil {
		t.Fatal(err)
	}
	if hit.Found || !hit.Quarantined {
		t.Fatalf("corrupt metadata hit = %+v", hit)
	}
}

func TestCacheKeyDeterministic(t *testing.T) {
	c := newCache(t)
	a := c.Key("i", "c")
	b := c.Key("i", "c")
	if a != b || len(a) != 64 {
		t.Fatalf("key = %q", a)
	}
	if c.Key("i", "c2") == a {
		t.Fatal("key ignores config")
	}
}
