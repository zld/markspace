package atomicio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteFileAtomic(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "nested", "out.vtt")

	// Parent directory must be created by the caller; WriteFile itself only
	// manages the temp-file dance, so create it first.
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("WEBVTT\n\nhello\n")
	if err := WriteFile(target, payload, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Errorf("content mismatch: %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Errorf("perm = %o, want 0644", info.Mode().Perm())
	}

	// No temp files may remain in the target directory.
	entries, err := os.ReadDir(filepath.Dir(target))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.HasPrefix(e.Name(), ".subsync-tmp-") {
			t.Errorf("temp file left behind: %s", e.Name())
		}
	}
}

func TestWriteFileBadPathLeavesNoTrace(t *testing.T) {
	dir := t.TempDir()
	// A target inside a non-existent directory cannot be created; the temp
	// file creation fails outright and nothing is left behind.
	target := filepath.Join(dir, "missing-dir", "x.vtt")
	if err := WriteFile(target, []byte("data"), 0o644); err == nil {
		t.Fatal("expected error for missing directory")
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("unexpected leftovers: %v", entries)
	}
}

func TestReadLimited(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "in")
	if err := os.WriteFile(path, make([]byte, MaxInputBytes+10), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := ReadLimited(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != MaxInputBytes+1 {
		t.Errorf("read %d bytes, want cap %d", len(got), MaxInputBytes+1)
	}
}
