package manifest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteFileAtomicReplacesContent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "media_800.m3u8")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := WriteFileAtomic(path, []byte("new")); err != nil {
		t.Fatalf("WriteFileAtomic: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("content = %q", data)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary files left behind: %v", entries)
	}
}

func TestWriteFileAtomicFailureKeepsOriginal(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure cannot be simulated as root")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "media_800.m3u8")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

	err := WriteFileAtomic(path, []byte("replacement"))
	if err == nil {
		t.Fatal("WriteFileAtomic succeeded in a read-only directory")
	}
	if !WriteTempError(err) || !strings.Contains(err.Error(), "temporary manifest") {
		t.Fatalf("error = %v", err)
	}
	data, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "original" {
		t.Fatalf("original content = %q", data)
	}
}
