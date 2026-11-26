package manifest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"liveregress/internal/fault"
)

func TestSecureJoinAcceptsNestedRelative(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "media", "v0"), 0o755)
	os.WriteFile(filepath.Join(root, "media", "v0", "seg1.m4s"), []byte("x"), 0o644)

	abs, rel, err := SecureJoin(root, "media/v0/seg1.m4s")
	if err != nil {
		t.Fatalf("SecureJoin: %v", err)
	}
	if rel != "media/v0/seg1.m4s" {
		t.Fatalf("rel = %q", rel)
	}
	if !filepath.IsAbs(abs) {
		t.Fatalf("abs = %q", abs)
	}
}

func TestSecureJoinRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	os.WriteFile(outside, []byte("x"), 0o644)
	defer os.Remove(outside)

	for _, uri := range []string{
		"../outside.txt",
		"media/../../outside.txt",
		"%2e%2e/outside.txt",
		"%2E%2E%2Foutside.txt",
		"/etc/passwd",
		"file:///etc/passwd",
		"http://example.com/x",
		"media/seg.m4s?x=1",
		`..\outside.txt`,
		".",
		"",
	} {
		_, _, err := SecureJoin(root, uri)
		if err == nil {
			t.Fatalf("SecureJoin(%q) unexpectedly succeeded", uri)
		}
		if !errors.Is(err, fault.ErrPath) {
			t.Fatalf("SecureJoin(%q) = %v, want path fault", uri, err)
		}
	}
}

func TestSecureJoinRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	os.WriteFile(filepath.Join(outsideDir, "secret"), []byte("x"), 0o644)
	if err := os.Symlink(outsideDir, filepath.Join(root, "link")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := SecureJoin(root, "link/secret"); err == nil {
		t.Fatal("symlink escape accepted")
	}
}

func TestSecureJoinAllowsSymlinkInsideRoot(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "real"), 0o755)
	os.WriteFile(filepath.Join(root, "real", "f"), []byte("x"), 0o644)
	if err := os.Symlink("real", filepath.Join(root, "alias")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, _, err := SecureJoin(root, "alias/f"); err != nil {
		t.Fatalf("internal symlink rejected: %v", err)
	}
}
