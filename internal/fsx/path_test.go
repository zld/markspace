package fsx

import (
	"os"
	"path/filepath"
	"testing"

	"mediakernel/internal/clock"
)

func TestResolveAcceptsContainedPaths(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := Resolve(base, filepath.Join("sub", "out.wav"))
	if err != nil {
		t.Fatalf("contained path rejected: %v", err)
	}
	if !inside(base, got) {
		t.Fatalf("resolved %s outside base", got)
	}
	if _, err := Resolve(base, base); err != nil {
		t.Fatalf("base itself rejected: %v", err)
	}
}

func TestResolveRejectsLexicalEscape(t *testing.T) {
	base := t.TempDir()
	if _, err := Resolve(base, filepath.Join(base, "..", "evil.wav")); err == nil {
		t.Fatal("../ escape must be rejected")
	} else if ce, ok := clock.AsError(err); !ok || ce.Category != clock.CatSymlinkEscape {
		t.Fatalf("want symlink_escape category, got %v", err)
	}
}

func TestResolveRejectsSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.wav")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link.wav")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Resolve(base, link); err == nil {
		t.Fatal("out-of-base symlink must be rejected")
	} else if ce, ok := clock.AsError(err); !ok || ce.Category != clock.CatSymlinkEscape {
		t.Fatalf("want symlink_escape, got %v", err)
	}
}

func TestResolveAllowsInternalSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real.wav")
	if err := os.WriteFile(real, []byte("RIFF"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "alias.wav")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	f, _, err := OpenInput(base, link)
	if err != nil {
		t.Fatalf("internal symlink should open: %v", err)
	}
	_ = f.Close()
}

func TestEnsureOutputCreatesParent(t *testing.T) {
	base := t.TempDir()
	out, err := EnsureOutput(base, filepath.Join("nested", "deep", "r.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(out)); err != nil {
		t.Fatalf("parent not created: %v", err)
	}
}
