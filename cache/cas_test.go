package cache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCASHitRereadsAndVerifies(t *testing.T) {
	dir := t.TempDir()
	c, err := New(filepath.Join(dir, "cas"))
	if err != nil {
		t.Fatal(err)
	}
	payload := []byte("fragment-bytes")
	calls := 0
	read := func(context.Context) (Snapshot, error) {
		calls++
		return Snapshot{SourceID: "dash-high/seg-1", Data: payload}, nil
	}

	r1, data, err := c.Ensure(context.Background(), "dash-high/seg-1", SHA256Hex(payload), read)
	if err != nil || r1.CacheHit || string(data) != string(payload) {
		t.Fatalf("first ensure: r=%+v err=%v", r1, err)
	}
	r2, data, err := c.Ensure(context.Background(), "dash-high/seg-1", SHA256Hex(payload), read)
	if err != nil || !r2.CacheHit || !r2.SourceVerified || r2.Reingested {
		t.Fatalf("second ensure should hit: %+v err=%v", r2, err)
	}
	if calls != 2 {
		t.Fatalf("source was not reread on cache hit: calls=%d", calls)
	}
	if string(data) != string(payload) {
		t.Fatal("cached data mismatch")
	}
}

func TestCASMismatchRejectsAndReingests(t *testing.T) {
	c, err := New(filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatal(err)
	}
	good := []byte("good")
	if _, err := c.Put(context.Background(), "src", good); err != nil {
		t.Fatal(err)
	}
	claimed := SHA256Hex(good)
	corrupt := []byte("corrupt")
	r, _, err := c.Ensure(context.Background(), "src", claimed, func(context.Context) (Snapshot, error) {
		return Snapshot{SourceID: "src", Data: corrupt}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Reingested || r.SourceVerified || r.Entry.Digest != SHA256Hex(corrupt) {
		t.Fatalf("mismatch was not reingested: %+v", r)
	}
	if _, err := os.Stat(r.Entry.Path); err != nil {
		t.Fatalf("new object not persisted: %v", err)
	}
}

func TestCASCachedMetadataRejectsUnknownSource(t *testing.T) {
	c, _ := New(filepath.Join(t.TempDir(), "cas"))
	data := []byte("same-content")
	if _, err := c.Put(context.Background(), "source-a", data); err != nil {
		t.Fatal(err)
	}
	r, _, err := c.Ensure(context.Background(), "source-b", SHA256Hex(data), func(context.Context) (Snapshot, error) {
		return Snapshot{SourceID: "source-b", Data: data}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Reingested || r.CacheHit || r.SourceVerified {
		t.Fatalf("unknown cached source should force reingest: %+v", r)
	}
}

func TestCASSourceIdentifierMismatch(t *testing.T) {
	c, _ := New(filepath.Join(t.TempDir(), "cas"))
	data := []byte("x")
	r, _, err := c.Ensure(context.Background(), "expected", SHA256Hex(data), func(context.Context) (Snapshot, error) {
		return Snapshot{SourceID: "other", Data: data}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !r.Reingested || r.Reason == "" {
		t.Fatalf("expected source mismatch remediation, got %+v", r)
	}
}
