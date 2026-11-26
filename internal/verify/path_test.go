package verify_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"regress/internal/verify"
)

func tamperManifest(t *testing.T, root, from, to string) {
	t.Helper()
	manifestPath := filepath.Join(root, "manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	out := strings.Replace(string(raw), from, to, 1)
	if err := os.WriteFile(manifestPath, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRejectsPathTraversal ensures a manifest entry cannot reference files
// outside the job root through "../" traversal.
func TestRejectsPathTraversal(t *testing.T) {
	_, _, root := buildPublished(t, "h264")
	tamperManifest(t, root, "h264-0.m4s", "../../escape.m4s")
	if _, err := verify.NewChecker(root).Check(context.Background(), verify.Expectations{
		SSIMMin: 0.8, MaxBitrateBPS: 6_000_000, MeasuredSSIM: 0.95,
	}); err == nil {
		t.Fatal("path traversal entry was accepted")
	}
}

// TestRejectsAbsolutePath ensures absolute manifest entries are rejected.
func TestRejectsAbsolutePath(t *testing.T) {
	_, _, root := buildPublished(t, "h264")
	tamperManifest(t, root, "h264-0.m4s", "/etc/passwd")
	if _, err := verify.NewChecker(root).Check(context.Background(), verify.Expectations{
		SSIMMin: 0.8, MaxBitrateBPS: 6_000_000, MeasuredSSIM: 0.95,
	}); err == nil {
		t.Fatal("absolute path entry was accepted")
	}
}
