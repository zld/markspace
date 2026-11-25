package store

import (
	"os"
	"path/filepath"
	"testing"
)

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSourceIsImmutable(t *testing.T) {
	root := t.TempDir()
	srcDir := t.TempDir()
	a := filepath.Join(srcDir, "a.mp4")
	write(t, a, "original")
	ss := NewSourceStore(root)
	first, err := ss.Put("inputs/a.mp4", a)
	if err != nil {
		t.Fatal(err)
	}
	// Same bytes: reuse, digest unchanged.
	again, err := ss.Put("inputs/a.mp4", a)
	if err != nil || again.Digest != first.Digest {
		t.Fatalf("reuse failed: %v", err)
	}
	// Different bytes under the same name must be refused, not overwritten.
	write(t, a, "changed")
	if _, err := ss.Put("inputs/a.mp4", a); err == nil {
		t.Fatal("expected sealed asset to refuse rewrite")
	}
}

func TestSourceRejectsPathEscape(t *testing.T) {
	ss := NewSourceStore(t.TempDir())
	src := filepath.Join(t.TempDir(), "x")
	write(t, src, "x")
	for _, name := range []string{"../escape", "a/../../escape", "/abs"} {
		if _, err := ss.Put(name, src); err == nil {
			t.Fatalf("name %q should be rejected", name)
		}
	}
}
