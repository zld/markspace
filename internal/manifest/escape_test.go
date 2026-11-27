package manifest_test

import (
	"os"
	"path/filepath"
	"testing"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
	"archivecheck/internal/manifest"
)

func TestVerifyRejectsSymlinkedDerivative(t *testing.T) {
	dir := t.TempDir()
	src := asset.FromBytes("a.srt", []byte("q"))
	b := manifest.NewBuilder(dir)
	_ = b.AddSource(src)
	d, err := b.AddDerivative(manifest.DerivativeSpec{Kind: "subtitle", Layer: 1, Data: []byte(`{}`), Deps: []string{src.Digest}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Build(); err != nil {
		t.Fatal(err)
	}
	// Replace the stored derivative with a symlink pointing outside the root.
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, filepath.FromSlash(d.Path))
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := manifest.Verify(dir); !fault.HasKind(err, fault.KindBoundary) {
		t.Fatalf("symlinked derivative accepted: %v", err)
	}
}
