package index

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

func TestStorePendingToPublishedAndPersist(t *testing.T) {
	p := filepath.Join(t.TempDir(), "index.json")
	s, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	key := ProductKey("source-a", "track-1")
	if _, created, err := s.EnsurePending(Artifact{Key: key, SourceSHA256: "abc"}); err != nil || !created {
		t.Fatalf("EnsurePending created=%v err=%v", created, err)
	}
	if _, created, err := s.EnsurePending(Artifact{Key: key}); err != nil || created {
		t.Fatalf("second EnsurePending created=%v err=%v", created, err)
	}
	if _, err := s.MarkPublished(key, "/tmp/a.mp4", "deadbeef", 42); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkPublished(key, "/tmp/a.mp4", "deadbeef", 42); !errors.Is(err, ErrAlreadyPublished) {
		t.Fatalf("repeat publish err=%v want ErrAlreadyPublished", err)
	}

	s2, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	a, ok := s2.Get(key)
	if !ok || a.State != StatePublished || a.OutputSHA256 != "deadbeef" {
		t.Fatalf("persisted artifact=%+v ok=%v", a, ok)
	}
}

func TestStoreRepairKeepsOnePublishedArtifact(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "i.json"))
	key := "repair-key"
	if _, _, err := s.EnsurePending(Artifact{Key: key}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkPublished(key, "/tmp/a.mp4", "old", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReplacePublished(key, "/tmp/a.mp4", "new", 2); err != nil {
		t.Fatal(err)
	}
	a, ok := s.Get(key)
	if !ok || a.State != StatePublished || a.OutputSHA256 != "new" {
		t.Fatalf("artifact=%+v ok=%v", a, ok)
	}
	if len(s.Records) != 1 {
		t.Fatalf("records=%d want one per key", len(s.Records))
	}
}

func TestStoreKeyedMutexSerializesSameKey(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "i.json"))
	key := "deterministic-key"
	var wg sync.WaitGroup
	var active, maxActive int
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l := s.LockKey(key)
			l.Lock()
			defer l.Unlock()
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			mu.Unlock()
			mu.Lock()
			active--
			mu.Unlock()
		}()
	}
	wg.Wait()
	if maxActive != 1 {
		t.Fatalf("same key critical section concurrency=%d", maxActive)
	}
	if s.LockKey("other") == s.LockKey(key) {
		t.Fatal("different keys must use different locks")
	}
}
