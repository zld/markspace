package safeio

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJoinRejectsTraversal(t *testing.T) {
	r, err := NewRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	good, err := r.Join("rendition", "seg-1.ts")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(good) != filepath.Join(r.String(), "rendition") {
		t.Fatalf("unexpected path %s", good)
	}
	for _, bad := range []string{"../escape.ts", "a/../../b.ts", "/etc/passwd"} {
		if _, err := r.Join(bad); err == nil {
			t.Fatalf("expected rejection for %q", bad)
		}
	}
}

func TestAtomicWrite(t *testing.T) {
	r, err := EnsureRoot(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	if err := r.AtomicWrite("pl/index.m3u8", []byte("#EXTM3U\n")); err != nil {
		t.Fatal(err)
	}
	got, err := r.ReadFile("pl/index.m3u8")
	if err != nil || string(got) != "#EXTM3U\n" {
		t.Fatalf("read back %q err=%v", got, err)
	}
	entries, err := os.ReadDir(filepath.Join(r.String(), "pl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == "" && e.Name()[0] == '.' {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestRootRejectsFile(t *testing.T) {
	f := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRoot(f); err == nil {
		t.Fatal("expected error when root is a file")
	}
}
