package safeio

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/fault"
)

func TestSafeJoinAllowsNormalPaths(t *testing.T) {
	root := t.TempDir()
	got, err := SafeJoin(root, filepath.Join("seg", "0001.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(got) != filepath.Join(root, "seg") {
		t.Fatalf("joined path wrong: %s", got)
	}
}

func TestSafeJoinRejectsTraversalAndAbsolute(t *testing.T) {
	root := t.TempDir()
	bad := []string{
		"../escape.mp4",
		"a/../../escape",
		"/etc/passwd",
	}
	for _, name := range bad {
		if _, err := SafeJoin(root, name); err == nil {
			t.Fatalf("unsafe path accepted: %q", name)
		} else if fe, ok := fault.As(err); !ok || fe.Category != fault.CatUnsafePath {
			t.Fatalf("wrong fault for %q: %v", name, err)
		}
	}
}

func TestSafeJoinRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "evil")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := SafeJoin(root, filepath.Join("evil", "x.mp4")); err == nil {
		t.Fatal("symlink escape was allowed")
	} else if fe, ok := fault.As(err); !ok || fe.Category != fault.CatUnsafePath {
		t.Fatalf("wrong fault: %v", err)
	}
}

func TestAtomicWriteReplacesContents(t *testing.T) {
	root := t.TempDir()
	dst := filepath.Join(root, "index.json")
	if err := AtomicWrite(dst, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(dst, []byte("second")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "second" {
		t.Fatalf("content = %q", got)
	}
	// No temp files may linger.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if name := e.Name(); len(name) >= 4 && name[:4] == ".tmp" {
			t.Fatalf("temporary file left behind: %s", name)
		}
	}
}

func TestEnsureRootCreatesAndRejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "out")
	abs, err := EnsureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		t.Fatalf("root not created: %v", err)
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(root, link); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRoot(link); err == nil {
		t.Fatal("symlink root was accepted")
	}
}
