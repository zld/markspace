package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteAtomicAndVerify(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "timeline.json")
	data := []byte(`{"ok":true}` + "\n")

	sha, err := WriteAtomic(path, data)
	if err != nil {
		t.Fatal(err)
	}
	if sha != HashBytes(data) {
		t.Fatal("returned hash differs from HashBytes")
	}
	gotSHA, size, err := Verify(path, sha, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if gotSHA != sha || size != int64(len(data)) {
		t.Fatalf("verify=%s,%d", gotSHA, size)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestVerifyDetectsCorruption(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.json")
	if _, err := WriteAtomic(path, []byte("v1")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Verify(path, HashBytes([]byte("v1")), -1); err == nil {
		t.Fatal("expected hash mismatch")
	}
}
