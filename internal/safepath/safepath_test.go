package safepath_test

import (
	"os"
	"path/filepath"
	"testing"

	"mediaflow/internal/safepath"
)

func TestResolveRejectsTraversalAndAbsolute(t *testing.T) {
	root := t.TempDir()
	if _, err := safepath.Resolve(root, "media", "a.mp4"); err != nil {
		t.Fatalf("normal relative path rejected: %v", err)
	}
	if _, err := safepath.Resolve(root, "..", "escape.mp4"); err == nil {
		t.Fatal("expected parent traversal rejection")
	}
	if _, err := safepath.Resolve(root, "/tmp/absolute.mp4"); err == nil {
		t.Fatal("expected absolute path rejection")
	}
}

func TestCheckWithinAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "manifest.json")
	if _, err := safepath.CheckWithin(root, inside); err != nil {
		t.Fatalf("inside path rejected: %v", err)
	}
	outside := filepath.Join(root, "..", "escape.json")
	if _, err := safepath.CheckWithin(root, outside); err == nil {
		t.Fatal("expected traversal outside root to be rejected")
	}

	outsideDir := t.TempDir()
	realOutside := filepath.Join(outsideDir, "real.mp4")
	if err := os.WriteFile(realOutside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link.mp4")
	if err := os.Symlink(realOutside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := safepath.CheckExistingWithin(root, link); err == nil {
		t.Fatal("expected symlink escape to be rejected")
	}
}
