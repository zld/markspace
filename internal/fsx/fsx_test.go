package fsx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeJoinRejectsEscape(t *testing.T) {
	root := t.TempDir()
	if _, err := SafeJoin(root, "../secret.png"); err == nil {
		t.Fatal("expected parent escape rejection")
	}
	if _, err := SafeJoin(root, "/etc/passwd"); err == nil {
		t.Fatal("expected absolute path rejection")
	}
	got, err := SafeJoin(root, filepath.Join("frames", "a.png"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, root) {
		t.Fatalf("joined path %q outside %q", got, root)
	}
}

func TestAtomicWriteAndStageCommit(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "file.txt")
	if err := AtomicWriteFile(path, strings.NewReader("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || string(b) != "hello" {
		t.Fatalf("read=%q err=%v", b, err)
	}

	stage := filepath.Join(root, "stage.tmp")
	final := filepath.Join(root, "final.txt")
	if err := WriteSynced(stage, strings.NewReader("block"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := CommitStage(stage, final); err != nil {
		t.Fatal(err)
	}
	b, err = os.ReadFile(final)
	if err != nil || string(b) != "block" {
		t.Fatalf("final=%q err=%v", b, err)
	}
	if _, err := os.Stat(stage); !os.IsNotExist(err) {
		t.Fatalf("stage should be consumed, stat err=%v", err)
	}
}
