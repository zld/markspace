package migrate

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"streamcheck/core"
)

func setup(t *testing.T) (src, dst string, srcBytes []byte) {
	t.Helper()
	dir := t.TempDir()
	src = filepath.Join(dir, "old.m3u8")
	dst = filepath.Join(dir, "new.json")
	srcBytes = []byte("#EXTM3U\n#EXTINF:2.0,\na.ts\n")
	if err := os.WriteFile(src, srcBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	return src, dst, srcBytes
}

func upper(b []byte) ([]byte, error) {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			c -= 32
		}
		out[i] = c
	}
	return out, nil
}

func TestMigrationCommit(t *testing.T) {
	src, dst, srcBytes := setup(t)
	m := &Migration{
		Src: src, Dst: dst,
		Transform: upper,
		Validate:  func(b []byte) error { return nil },
	}
	if err := m.Run(); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "#EXTM3U\n#EXTINF:2.0,\nA.TS\n" {
		t.Fatalf("dst: %q", got)
	}
	// Old format stays readable and untouched.
	old, err := os.ReadFile(src)
	if err != nil || string(old) != string(srcBytes) {
		t.Fatalf("src changed: %q %v", old, err)
	}
	// No staged files left behind.
	entries, _ := os.ReadDir(filepath.Dir(src))
	for _, e := range entries {
		if len(e.Name()) > 8 && e.Name()[:9] == ".migrate-" {
			t.Fatalf("staged file left: %s", e.Name())
		}
	}
}

func TestMigrationValidationFailureRollsBack(t *testing.T) {
	src, dst, srcBytes := setup(t)
	boom := errors.New("injected validation failure")
	m := &Migration{
		Src: src, Dst: dst,
		Transform: upper,
		Validate:  func(b []byte) error { return boom },
	}
	err := m.Run()
	if err == nil {
		t.Fatal("must fail")
	}
	e, ok := err.(*core.Error)
	if !ok || e.Kind != core.KindMigrate {
		t.Fatalf("structured error: %v", err)
	}
	// Destination never appeared.
	if _, statErr := os.Stat(dst); !os.IsNotExist(statErr) {
		t.Fatal("dst must not exist after rollback")
	}
	// Source byte-identical.
	old, _ := os.ReadFile(src)
	if sha256.Sum256(old) != sha256.Sum256(srcBytes) {
		t.Fatal("source bytes differ after rollback")
	}
	// No staged residue.
	entries, _ := os.ReadDir(filepath.Dir(src))
	for _, en := range entries {
		if len(en.Name()) > 8 && en.Name()[:9] == ".migrate-" {
			t.Fatalf("staged file left: %s", en.Name())
		}
	}
}

func TestRollbackDetectsSourceTamper(t *testing.T) {
	src, dst, _ := setup(t)
	m := &Migration{Src: src, Dst: dst, Transform: upper, Validate: func(b []byte) error { return nil }}
	old, err := m.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Shadow(old); err != nil {
		t.Fatal(err)
	}
	// Simulate an interrupted run where the source was modified.
	if err := os.WriteFile(src, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Rollback(); err == nil {
		t.Fatal("rollback must detect source change")
	}
}

func TestCommitWithoutStageFails(t *testing.T) {
	m := &Migration{Src: "x", Dst: "y"}
	if err := m.Commit(); err == nil {
		t.Fatal("commit without shadow must fail")
	}
}
