package fsutil

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSafeJoinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	bad := []string{"..", "../secret", "a/../../b", "/etc/passwd", "x/y"}
	for _, name := range bad {
		if _, err := SafeJoin(root, name); !errors.Is(err, ErrPathEscape) {
			t.Fatalf("SafeJoin(root, %q) err=%v, want ErrPathEscape", name, err)
		}
	}
}

func TestSafeJoinAllowsBasenames(t *testing.T) {
	root := t.TempDir()
	got, err := SafeJoin(root, "seg_0.ts")
	if err != nil {
		t.Fatalf("SafeJoin: %v", err)
	}
	want := filepath.Join(root, "seg_0.ts")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestSafeJoinRelRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	for _, rel := range []string{"../x", "a/../../b", "/abs/path", "a/../b/.."} {
		if _, err := SafeJoinRel(root, rel); !errors.Is(err, ErrPathEscape) {
			t.Fatalf("SafeJoinRel(%q) err=%v, want ErrPathEscape", rel, err)
		}
	}
}

func TestSafeJoinRelNested(t *testing.T) {
	root := t.TempDir()
	got, err := SafeJoinRel(root, "rendition_1/playlist.m3u8")
	if err != nil {
		t.Fatalf("SafeJoinRel: %v", err)
	}
	if filepath.Clean(got) != filepath.Join(root, "rendition_1", "playlist.m3u8") {
		t.Fatalf("unexpected path %q", got)
	}
}

func TestAtomicWriteFileReplaces(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "playlist.m3u8")
	if err := AtomicWriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := AtomicWriteFile(path, []byte("new"), 0o644); err != nil {
		t.Fatalf("second write: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatalf("content=%q err=%v", data, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == "" && len(e.Name()) > 4 {
			// No leftover temp files should remain.
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}
