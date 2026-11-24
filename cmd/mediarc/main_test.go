package main

import (
	"path/filepath"
	"testing"
)

func TestDemoWritesOnceAndRefusesOverwrite(t *testing.T) {
	out := filepath.Join(t.TempDir(), "out.mp4")
	code := run([]string{"demo", "--out", out, "--seed", "7", "--loss", "8", "--late-ms", "120"})
	if code != 0 {
		t.Fatalf("first demo exit = %d, want 0", code)
	}
	code = run([]string{"demo", "--out", out})
	if code != 2 {
		t.Fatalf("overwrite exit = %d, want 2", code)
	}
}

func TestVerifyCaseExitCodes(t *testing.T) {
	for _, c := range []string{"truncated", "late-boundary", "color-conflict", "loss-over-window"} {
		if code := run([]string{"verify", "--case", c}); code != 2 {
			t.Fatalf("%s exit = %d, want 2", c, code)
		}
	}
}

func TestUnknownVerifyCaseUsage(t *testing.T) {
	if code := run([]string{"verify", "--case", "nope"}); code == 0 {
		t.Fatal("unknown case unexpectedly succeeded")
	}
}
