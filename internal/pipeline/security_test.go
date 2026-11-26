package pipeline

import (
	"path/filepath"
	"strings"
	"testing"

	"continuity/internal/clock"
	"continuity/internal/manifest"
)

func TestRunnerRejectsURLInputs(t *testing.T) {
	cases := []Config{
		{InputDir: "https://evil.test/x", IndexDir: "/tmp/i", OutputDir: "/tmp/o"},
		{InputDir: "/tmp/in", IndexDir: "//host/share", OutputDir: "/tmp/o"},
	}
	for _, cfg := range cases {
		cfg.Chunk = 2
		if _, err := NewRunner(cfg); err == nil {
			t.Errorf("expected rejection for %+v", cfg)
		} else if clock.KindOf(err) != clock.KindInvalidInput {
			t.Errorf("kind = %s, want invalid_input", clock.KindOf(err))
		}
	}
}

func TestSafePathTraversalBlocked(t *testing.T) {
	root := t.TempDir()
	refs := []string{"../escape.m4s", "a/../../b.m4s", "/etc/passwd",
		"http://x/y.m4s"}
	for _, ref := range refs {
		if _, err := manifest.SafePath(root, ref); err == nil {
			t.Errorf("traversal/URL ref %q was accepted", ref)
		}
	}
	good, err := manifest.SafePath(root, filepath.Join("a", "b.m4s"))
	if err != nil || !strings.HasPrefix(good, root) {
		t.Fatalf("safe ref resolved badly: %q err=%v", good, err)
	}
}

func TestConcurrencyAndSegmentCeiling(t *testing.T) {
	if maxWorkers != 4 {
		t.Errorf("maxWorkers = %d, want 4", maxWorkers)
	}
	if manifest.MaxSegmentBytes != 64<<20 {
		t.Errorf("MaxSegmentBytes = %d, want 64 MiB", manifest.MaxSegmentBytes)
	}
}

func TestChunkDefaultsToTwo(t *testing.T) {
	r, err := NewRunner(Config{
		InputDir: t.TempDir(), IndexDir: filepath.Join(t.TempDir(), "i"),
		OutputDir: filepath.Join(t.TempDir(), "o"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if r.cfg.Chunk != 2 {
		t.Errorf("default chunk = %d, want 2", r.cfg.Chunk)
	}
}
