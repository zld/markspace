package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/model"
	"mediad/internal/segment"
)

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeManifest(t *testing.T, dir string, m *segment.Manifest) {
	t.Helper()
	if err := m.Seal(); err != nil {
		t.Fatal(err)
	}
	raw, err := m.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyAcceptsIntactRun(t *testing.T) {
	dir := t.TempDir()
	seg := &segment.Descriptor{
		Representation: "video-high", Index: 0,
		ByteSize: 3, SHA256: shaHex([]byte("abc")),
	}
	if err := os.WriteFile(filepath.Join(dir, "video-high-seg-00000.m4s"),
		[]byte("abc"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := segment.NewManifest(1000, nil, []*segment.Descriptor{seg}, 0, nil)
	writeManifest(t, dir, m)

	res, err := Run(dir)
	if err != nil {
		t.Fatalf("intact run must verify: %v", err)
	}
	if res.SegmentsChecked != 1 || len(res.ManifestSHA256) != 64 {
		t.Fatalf("bad result: %+v", res)
	}
}

func TestVerifyDetectsTamperedSegment(t *testing.T) {
	dir := t.TempDir()
	seg := &segment.Descriptor{
		Representation: "video-low", Index: 1,
		ByteSize: 3, SHA256: shaHex([]byte("abc")),
	}
	if err := os.WriteFile(filepath.Join(dir, "video-low-seg-00001.m4s"),
		[]byte("xyz"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := segment.NewManifest(1000, nil, []*segment.Descriptor{seg}, 0, nil)
	writeManifest(t, dir, m)

	if _, err := Run(dir); !errors.Is(err, model.ErrChecksum) {
		t.Fatalf("expected ErrChecksum, got %v", err)
	}
}

func TestVerifyDetectsTamperedManifest(t *testing.T) {
	dir := t.TempDir()
	m := segment.NewManifest(1000, nil, nil, 0, nil)
	writeManifest(t, dir, m)
	// Mutate the sealed manifest in place.
	path := filepath.Join(dir, "manifest.json")
	raw, _ := os.ReadFile(path)
	raw[len(raw)-20] = '0'
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Run(dir); !errors.Is(err, model.ErrChecksum) {
		t.Fatalf("expected manifest self-digest failure, got %v", err)
	}
}

func TestVerifyMissingManifest(t *testing.T) {
	if _, err := Run(t.TempDir()); err == nil {
		t.Fatal("missing manifest must fail verification")
	}
}
