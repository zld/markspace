package cache

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/local/tracecheck/internal/clock"
)

func newCache(t *testing.T) *CAS {
	t.Helper()
	c, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestPutThenHit(t *testing.T) {
	c := newCache(t)
	src := Source{InputID: "input-A", Start: 0, End: 10}
	content := []byte("segment-bytes")
	key, err := c.Put(src, content)
	if err != nil {
		t.Fatal(err)
	}
	rec, mismatch := c.Lookup(src, content)
	if mismatch != nil || rec == nil {
		t.Fatalf("expected hit, got rec=%v mismatch=%+v", rec, mismatch)
	}
	if rec.SHA256 != key || c.Hits != 1 || c.Misses != 0 {
		t.Fatalf("counters hits=%d misses=%d key=%s", c.Hits, c.Misses, rec.SHA256)
	}
}

func TestMissOnAbsentEntry(t *testing.T) {
	c := newCache(t)
	rec, mismatch := c.Lookup(Source{InputID: "x"}, []byte("never-stored"))
	if rec != nil || mismatch != nil || c.Misses != 1 || c.Hits != 0 {
		t.Fatalf("expected plain miss, rec=%v mismatch=%+v", rec, mismatch)
	}
}

func TestSourceIdentityMismatch(t *testing.T) {
	c := newCache(t)
	content := []byte("same-bytes")
	if _, err := c.Put(Source{InputID: "input-A", Start: 0, End: 9}, content); err != nil {
		t.Fatal(err)
	}
	// Same content bytes and range, but the input was swapped.
	rec, m := c.Lookup(Source{InputID: "input-B", Start: 0, End: 9}, content)
	if rec != nil || m == nil || m.Reason != clock.CodeCacheMismatch {
		t.Fatalf("expected source mismatch, rec=%v m=%+v", rec, m)
	}
	if c.Evicted != 1 || c.Hits != 0 {
		t.Fatalf("bad entry must be evicted: evicted=%d hits=%d", c.Evicted, c.Hits)
	}
	// After eviction the same provenance lookup is a plain miss.
	if _, m2 := c.Lookup(Source{InputID: "input-B", Start: 0, End: 9}, content); m2 != nil {
		t.Fatal("recomputed entry should no longer report a structural mismatch")
	}
}

func TestSourceRangeMismatch(t *testing.T) {
	c := newCache(t)
	content := []byte("range-bytes")
	if _, err := c.Put(Source{InputID: "in", Start: 0, End: 11}, content); err != nil {
		t.Fatal(err)
	}
	_, m := c.Lookup(Source{InputID: "in", Start: 5, End: 16}, content)
	if m == nil || m.Reason != clock.CodeCacheMismatch {
		t.Fatalf("expected range mismatch, got %+v", m)
	}
}

func TestTamperedBlobRejected(t *testing.T) {
	c := newCache(t)
	src := Source{InputID: "in", Start: 0, End: 5}
	content := []byte("abcde")
	key, err := c.Put(src, content)
	if err != nil {
		t.Fatal(err)
	}
	// Tamper the stored blob while leaving metadata intact.
	if err := os.WriteFile(filepath.Join(c.root, key+".bin"), []byte("ABCDE"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec, m := c.Lookup(src, content)
	if rec != nil || m == nil {
		t.Fatalf("tampered blob must be rejected, rec=%v m=%+v", rec, m)
	}
	if c.Evicted != 1 {
		t.Fatalf("tampered entry must be evicted, evicted=%d", c.Evicted)
	}
}

func TestCorruptMetadataRejected(t *testing.T) {
	c := newCache(t)
	content := []byte("meta-test")
	key, _ := c.Put(Source{InputID: "in"}, content)
	if err := os.WriteFile(filepath.Join(c.root, key+".json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, m := c.Lookup(Source{InputID: "in"}, content); m == nil {
		t.Fatal("corrupt metadata must produce a mismatch")
	}
}
