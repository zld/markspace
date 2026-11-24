package main

import (
	"path/filepath"
	"testing"
)

func TestCLIExitCodeContract(t *testing.T) {
	root := t.TempDir()

	if code := run([]string{"demo", "--seed", "7", "--out", filepath.Join(root, "ok")}); code != 0 {
		t.Fatalf("normal code=%d want0", code)
	}
	if code := run([]string{
		"demo", "--seed", "7", "--loss", "0.05", "--truncate", "13",
		"--missing-index", "--out", filepath.Join(root, "corrupt"),
	}); code != 2 {
		t.Fatalf("corrupt code=%d want2", code)
	}
	if code := run([]string{
		"demo", "--fail-node", "scan-mp4", "--out", filepath.Join(root, "blocked"),
	}); code != 3 {
		t.Fatalf("dependency code=%d want3", code)
	}
	if code := run([]string{"demo", "--loss", "not-a-float"}); code != 1 {
		t.Fatalf("flag parse code=%d want1", code)
	}
	if code := run([]string{"no-such-command"}); code != 1 {
		t.Fatalf("unknown command code=%d want1", code)
	}
}
