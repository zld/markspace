package workspace_test

import (
	"errors"
	"path/filepath"
	"testing"

	"mediacore/internal/diagnostic"
	"mediacore/internal/workspace"
)

func TestResolveInsideWorkRoot(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, "manifests")
	got, err := workspace.Resolve(root, manifestDir, "../media/seg-1.m4s")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "media", "seg-1.m4s")
	if got != want {
		t.Fatalf("resolved=%s want=%s", got, want)
	}
}

func TestResolveRejectsEscapeAndAbsolute(t *testing.T) {
	root := t.TempDir()
	_, err := workspace.Resolve(root, filepath.Join(root, "m"), "../../outside.m4s")
	if !errors.Is(err, diagnostic.ErrPathEscape) {
		t.Fatalf("relative escape err=%v", err)
	}
	_, err = workspace.Resolve(root, root, filepath.Join(string(filepath.Separator), "absolute.m4s"))
	if !errors.Is(err, diagnostic.ErrPathEscape) {
		t.Fatalf("absolute reference err=%v", err)
	}
}
