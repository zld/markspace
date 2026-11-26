package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"example.com/mediavault/internal/store"
)

// checkpointRef returns the content digest committed for a checkpoint block.
func checkpointRef(t *testing.T, dir string, block int) string {
	t.Helper()
	j, err := store.OpenJournal(dir)
	if err != nil {
		t.Fatal(err)
	}
	recs, err := j.Replay()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range recs {
		if r.Kind == "checkpoint" && r.Block == block {
			return r.ContentHash
		}
	}
	t.Fatalf("no checkpoint for block %d", block)
	return ""
}

func mustStore(t *testing.T, dir string) *store.Store {
	t.Helper()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func objectPath(t *testing.T, dir, digest string) string {
	t.Helper()
	p, err := mustStore(t, dir).Path(digest)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func storeRead(t *testing.T, dir, digest string) ([]byte, error) {
	t.Helper()
	return os.ReadFile(objectPath(t, dir, digest))
}

func storeWrite(t *testing.T, dir, digest string, b []byte) {
	t.Helper()
	if err := os.WriteFile(objectPath(t, dir, digest), b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func tamperObject(t *testing.T, dir, digest string) {
	t.Helper()
	b, err := storeRead(t, dir, digest)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) == 0 {
		t.Fatal("empty archive object")
	}
	b[len(b)/2] ^= 0xFF
	storeWrite(t, dir, digest, b)
}

var _ = filepath.Join
