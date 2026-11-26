package manifest_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediapack/internal/manifest"
)

func TestSaveLoadRoundtripAndVersion(t *testing.T) {
	dir := t.TempDir()
	m := manifest.New("source.ts", 2, []string{"r0", "r1"})
	m.Artifacts = append(m.Artifacts, manifest.Artifact{ID: "r0/s0", RelPath: "segments/r0/s0.frag", SHA256: "abc"})
	if err := m.Save(dir); err != nil {
		t.Fatal(err)
	}
	got, err := manifest.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.FormatVersion != 1 {
		t.Fatalf("format version=%d want 1", got.FormatVersion)
	}
	if len(got.Artifacts) != 1 || got.Artifacts[0].SHA256 != "abc" {
		t.Fatal("artifact roundtrip mismatch")
	}

	// A future/unknown format version must be rejected.
	p := filepath.Join(dir, manifest.ManifestName)
	b, _ := os.ReadFile(p)
	bad := strings.Replace(string(b), `"format_version": 1`, `"format_version": 999`, 1)
	if err := os.WriteFile(p, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.Load(dir); err == nil {
		t.Fatal("format_version 999 must be rejected")
	}
}

func TestResolveRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	if _, err := manifest.Resolve(dir, "../../etc/passwd"); err == nil {
		t.Fatal("traversal rel path must be rejected")
	}
	if _, err := manifest.Resolve(dir, "/etc/passwd"); err == nil {
		t.Fatal("absolute rel path must be rejected")
	}
	p, err := manifest.Resolve(dir, "segments/r0/a.frag")
	if err != nil || !strings.HasPrefix(p, dir) {
		t.Fatalf("valid resolve=%q err=%v", p, err)
	}
}

func TestHashBytesAndFile(t *testing.T) {
	sum, n := manifest.HashBytes([]byte("abc"))
	// sha256("abc")
	if sum != "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad" || n != 3 {
		t.Fatalf("hash=%s n=%d", sum, n)
	}
}
