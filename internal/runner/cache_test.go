package runner

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCacheKeyDependsOnBothDigests(t *testing.T) {
	base := Key("in-a", "cfg-a")
	if base == Key("in-b", "cfg-a") {
		t.Fatal("input digest must affect the key")
	}
	if base == Key("in-a", "cfg-b") {
		t.Fatal("config digest must affect the key")
	}
	if base != Key("in-a", "cfg-a") {
		t.Fatal("key must be deterministic")
	}
}

func TestCacheMissesOnChangedSegmentDigest(t *testing.T) {
	dir := t.TempDir()
	c := &Cache{Dir: dir}
	segs := map[string]string{"a.m4s": "111"}
	key := Key("in", "cfg")
	if err := c.Store(Entry{
		Key: key, InputDigest: "in", ConfigHash: "cfg", SegmentDigests: segs,
		SegmentsDone: 1, SegmentsTotal: 1, ReportDigest: "r",
	}); err != nil {
		t.Fatal(err)
	}
	if _, state, _ := c.Load(key, segs); state != CacheHit {
		t.Fatalf("expected hit, got %s", state)
	}
	changed := map[string]string{"a.m4s": "222"}
	if _, state, _ := c.Load(key, changed); state != CacheMiss {
		t.Fatalf("changed segment digest must miss, got %s", state)
	}
}

func TestCacheIncompleteEntryIsNotAHit(t *testing.T) {
	c := &Cache{Dir: t.TempDir()}
	key := Key("in", "cfg")
	if err := c.Store(Entry{
		Key: key, InputDigest: "in", ConfigHash: "cfg", SegmentDigests: map[string]string{},
		SegmentsDone: 1, SegmentsTotal: 4, ReportDigest: "",
	}); err != nil {
		t.Fatal(err)
	}
	if _, state, _ := c.Load(key, nil); state != CacheMiss {
		t.Fatalf("partial progress is not a cache hit, got %s", state)
	}
}

func TestCacheCorruptEntryIsAMissAndIsRemoved(t *testing.T) {
	dir := t.TempDir()
	c := &Cache{Dir: dir}
	key := Key("in", "cfg")
	path := filepath.Join(dir, key+".json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, state, _ := c.Load(key, nil); state != CacheMiss {
		t.Fatalf("corrupt entry must miss, got %s", state)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("corrupt entry should be removed")
	}
}

func TestPurgeForInputLeavesOtherInputsAlone(t *testing.T) {
	dir := t.TempDir()
	c := &Cache{Dir: dir}
	for _, in := range []string{"old", "new"} {
		if err := c.Store(Entry{
			Key: Key(in, "cfg"), InputDigest: in, ConfigHash: "cfg",
			SegmentDigests: map[string]string{}, SegmentsDone: 1, SegmentsTotal: 1, ReportDigest: "r",
		}); err != nil {
			t.Fatal(err)
		}
	}
	removed, err := c.PurgeForInput("old")
	if err != nil || removed != 1 {
		t.Fatalf("purged %d, err %v", removed, err)
	}
	if _, state, _ := c.Load(Key("new", "cfg"), nil); state != CacheHit {
		t.Fatal("entries for other inputs must survive")
	}
	if _, state, _ := c.Load(Key("old", "cfg"), nil); state != CacheMiss {
		t.Fatal("stale entry should be gone")
	}
}

func TestCacheDisabledWithoutDir(t *testing.T) {
	c := &Cache{}
	if _, state, _ := c.Load("k", nil); state != CacheDisabled {
		t.Fatalf("state = %s", state)
	}
	if err := c.Store(Entry{}); err != nil {
		t.Fatalf("disabled cache store must be a no-op: %v", err)
	}
}
