package asset_test

import (
	"os"
	"path/filepath"
	"testing"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
)

func TestSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	os.WriteFile(secret, []byte("secret"), 0o600)
	if err := os.Symlink(secret, filepath.Join(root, "link.txt")); err != nil {
		t.Skip(err)
	}
	src, err := asset.ReadAll(root, "link.txt", 1024)
	if err == nil {
		t.Fatalf("symlink escape was read: %q", string(src.Bytes()))
	}
	if !fault.HasKind(err, fault.KindBoundary) {
		t.Errorf("expected a boundary error, got %v", err)
	}
	// A symlink that stays inside the root is still readable.
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(inside, filepath.Join(root, "good-link.txt")); err != nil {
		t.Skip(err)
	}
	good, err := asset.ReadAll(root, "good-link.txt", 1024)
	if err != nil || string(good.Bytes()) != "ok" {
		t.Errorf("in-root symlink rejected: %v", err)
	}
	if _, err := asset.ReadAll(root, "missing-link.txt", 1024); !fault.HasKind(err, fault.KindMissing) {
		t.Errorf("missing path should be reported as missing, got %v", err)
	}
}
