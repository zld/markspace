package main

import (
	"path/filepath"
	"testing"
)

func TestRunArgumentErrorExitCode(t *testing.T) {
	if code := run([]string{"demo", "--out", ""}); code != 1 {
		t.Fatalf("empty --out code=%d, want 1", code)
	}
	if code := run([]string{"unknown-command"}); code != 1 {
		t.Fatalf("unknown command code=%d, want 1", code)
	}
}

func TestRunHeaderErrorExitCode(t *testing.T) {
	dir := t.TempDir()
	code := run([]string{
		"demo",
		"--out", dir,
		"--inject", "bad-header",
	})
	if code != 2 {
		t.Fatalf("bad header code=%d, want 2 (input=%s)", code, filepath.Join(dir, "input.mps"))
	}
}

func TestVerifyMissingManifestExitCode(t *testing.T) {
	if code := run([]string{"verify", "--out", t.TempDir()}); code != 1 {
		t.Fatalf("verify without manifest code=%d, want 1", code)
	}
}
