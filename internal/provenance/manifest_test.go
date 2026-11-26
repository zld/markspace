package provenance

import (
	"os"
	"path/filepath"
	"testing"

	"pwf/internal/ingest"
	"pwf/internal/media"
)

func TestManifestBuildAndVerify(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "cam.rtp")
	if err := os.WriteFile(src, []byte("source-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewBuilder()
	if err := b.AddSource("cam", src, "opus"); err != nil {
		t.Fatal(err)
	}
	b.AddRTPStream(&ingest.RTPStreamResult{
		Track:   "cam",
		Records: []ingest.RTPRecord{{Track: "cam", ExtSequence: 1, Offset: 0, PacketBytes: 12}},
		Gaps:    []ingest.GapRecord{{Track: "cam", StartSeq: 2, Count: 1}},
	})
	m, err := b.Build()
	if err != nil {
		t.Fatal(err)
	}
	if len(m.RunSHA256) != 64 {
		t.Fatalf("manifest digest must be 64 hex, got %d", len(m.RunSHA256))
	}
	mPath := filepath.Join(dir, "manifest.json")
	sha, err := m.Write(mPath)
	if err != nil || sha != m.RunSHA256 {
		t.Fatalf("manifest write failed: %v sha=%s", err, sha)
	}
	// Independent re-verification against the source directory.
	verified, err := Verify(mPath, dir)
	if err != nil {
		t.Fatalf("fresh manifest must verify: %v", err)
	}
	if verified.RunSHA256 != sha {
		t.Fatal("verified digest mismatch")
	}
}

func TestVerifyDetectsTamperedSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "cam.rtp")
	if err := os.WriteFile(src, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	b := NewBuilder()
	_ = b.AddSource("cam", src, "opus")
	m, _ := b.Build()
	mPath := filepath.Join(dir, "manifest.json")
	_, _ = m.Write(mPath)

	if err := os.WriteFile(src, []byte("changed-bytes"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Verify(mPath, dir)
	me, ok := media.AsError(err)
	if !ok || me.Kind != media.KindVerify {
		t.Fatalf("tampered source must be a verify error, got %v", err)
	}
}

func TestVerifyDetectsManifestTampering(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "cam.rtp")
	_ = os.WriteFile(src, []byte("data"), 0o644)
	b := NewBuilder()
	_ = b.AddSource("cam", src, "opus")
	m, _ := b.Build()
	mPath := filepath.Join(dir, "manifest.json")
	_, _ = m.Write(mPath)

	// Flip a byte in the stored manifest, invalidating its body digest.
	raw, _ := os.ReadFile(mPath)
	raw[len(raw)-20] ^= 0xff
	_ = os.WriteFile(mPath, raw, 0o644)
	if _, err := Verify(mPath, dir); err == nil {
		t.Fatal("tampered manifest must fail verification")
	}
}

func TestManifestDeterministic(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "c.rtp")
	_ = os.WriteFile(src, []byte("xyz"), 0o644)
	build := func() string {
		b := NewBuilder()
		_ = b.AddSource("c", src, "opus")
		m, _ := b.Build()
		return m.RunSHA256
	}
	if build() != build() {
		t.Fatal("manifest digest must be deterministic across builds")
	}
}
