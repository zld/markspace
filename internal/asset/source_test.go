package asset_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"archivecheck/internal/asset"
	"archivecheck/internal/fault"
)

func TestReadAllIsReadOnlyAndDigestAddressed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.bin"), []byte("hello archive"), 0o444); err != nil {
		t.Fatal(err)
	}
	s, err := asset.ReadAll(dir, "a.bin", 1024)
	if err != nil {
		t.Fatal(err)
	}
	if s.Size != 13 || len(s.Digest) != 64 {
		t.Fatalf("unexpected source %+v", s)
	}
	first := s.Digest
	s2, err := asset.ReadAll(dir, "a.bin", 1024)
	if err != nil || s2.Digest != first {
		t.Fatalf("digest not stable: %v %q %q", err, first, s2.Digest)
	}
	if err := s.Verify(first); err != nil {
		t.Errorf("verify: %v", err)
	}
	if err := s.Verify(strings.Repeat("0", 64)); fault.As(err) == nil || !fault.HasKind(err, fault.KindDigest) {
		t.Errorf("verify mismatch should be digest error: %v", err)
	}
}

func TestReadAllRejectsEscapesAndLimits(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "big.bin"), make([]byte, 32), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct{ root, path string }{
		{dir, "../secret.txt"},
		{dir, "sub/../../secret.txt"},
		{dir, outside},
	}
	for _, c := range cases {
		_, err := asset.ReadAll(c.root, c.path, 1024)
		if !fault.HasKind(err, fault.KindBoundary) {
			t.Errorf("%q: expected boundary error, got %v", c.path, err)
		}
	}
	if _, err := asset.ReadAll(dir, "big.bin", 16); !fault.HasKind(err, fault.KindLimit) {
		t.Errorf("expected limit error, got %v", err)
	}
	if _, err := asset.ReadAll(dir, "missing.bin", 16); !fault.HasKind(err, fault.KindMissing) {
		t.Errorf("expected missing error, got %v", err)
	}
}

func TestResolveRejectsAbsoluteAndStaysPut(t *testing.T) {
	root := t.TempDir()
	got, err := asset.Resolve(root, "sub/../a.bin")
	if err != nil || got != "a.bin" {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
	if _, err := asset.Resolve(root, "/etc/passwd"); !fault.HasKind(err, fault.KindBoundary) {
		t.Errorf("absolute path accepted: %v", err)
	}
	if _, err := asset.Resolve(root, " "); !fault.HasKind(err, fault.KindBoundary) {
		t.Errorf("empty path accepted: %v", err)
	}
	if !asset.Within(root, filepath.Join(root, "x")) || asset.Within(root, filepath.Dir(root)) {
		t.Error("Within is wrong")
	}
}

func TestFromBytesAndSlice(t *testing.T) {
	s := asset.FromBytes("sample.bin", []byte("0123456789"))
	if s.Size != 10 || s.Short() != s.Digest[:12] {
		t.Fatalf("unexpected %+v", s)
	}
	b, err := s.Slice(2, 5)
	if err != nil || string(b) != "234" {
		t.Fatalf("Slice = %q, %v", b, err)
	}
	if _, err := s.Slice(8, 99); !fault.HasKind(err, fault.KindBoundary) {
		t.Errorf("out-of-range slice accepted: %v", err)
	}
	// FromBytes must own its copy.
	src := []byte("abc")
	s = asset.FromBytes("x", src)
	src[0] = 'z'
	if string(s.Bytes()) != "abc" {
		t.Error("FromBytes aliased its input")
	}
}

func TestWriteAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "out.bin")
	sum, err := asset.WriteAtomic(path, []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != "payload" {
		t.Fatalf("read back = %q, %v", got, err)
	}
	if len(sum) != 64 {
		t.Errorf("digest %q", sum)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temporary file left behind: %s", e.Name())
		}
	}
}
