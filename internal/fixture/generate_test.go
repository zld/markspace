package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"archive-lab/internal/manifest"
	"archive-lab/internal/media"
	"archive-lab/internal/runner"
)

func TestGenerateAndProbe(t *testing.T) {
	dir := t.TempDir()
	written, err := Generate(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) == 0 {
		t.Fatal("nothing written")
	}
	// Determinism.
	dir2 := t.TempDir()
	written2, err := Generate(dir2)
	if err != nil {
		t.Fatal(err)
	}
	if len(written) != len(written2) {
		t.Fatalf("file count differs: %d vs %d", len(written), len(written2))
	}
	for i := range written {
		if written[i].SHA256 != written2[i].SHA256 {
			t.Fatalf("fixture %s is not deterministic", written[i].Path)
		}
	}
	// Every normal segment must probe cleanly and carry fragment timing.
	for i := 0; i < SegmentCount; i++ {
		path := filepath.Join(dir, "media", "normal", media.Name(i))
		p, err := media.ProbeFile(path, media.ProbeOptions{ExtractLuma: true})
		if err != nil {
			t.Fatalf("probe %s: %v", path, err)
		}
		if got := p.DurationNanos(); got != SegmentNanos {
			t.Fatalf("segment %d duration %d, want %d", i, got, SegmentNanos)
		}
		if len(p.Fragments) != 1 || len(p.Luma) != FramesPerSeg {
			t.Fatalf("segment %d fragments=%d luma=%d", i, len(p.Fragments), len(p.Luma))
		}
	}
	// The truncated segment must fail.
	trunc := filepath.Join(dir, "media", "truncated", "normal-0000.m4s")
	if _, err := media.ProbeFile(trunc, media.ProbeOptions{}); err == nil {
		t.Fatal("truncated fixture must not probe cleanly")
	}
	// Manifests parse; the malformed MPD does not.
	if _, err := runner.ParseManifest(filepath.Join(dir, "normal.m3u8"), mustRead(t, filepath.Join(dir, "normal.m3u8"))); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.ParseDASH("normal.mpd", mustRead(t, filepath.Join(dir, "normal.mpd")), manifest.Limits{}); err != nil {
		t.Fatal(err)
	}
	if _, err := manifest.ParseDASH("malformed.mpd", mustRead(t, filepath.Join(dir, "malformed.mpd")), manifest.Limits{}); err == nil {
		t.Fatal("malformed MPD must fail")
	}
	if _, err := manifest.ParseHLS("malformed.m3u8", mustRead(t, filepath.Join(dir, "malformed.m3u8")), manifest.Limits{}); err == nil {
		t.Fatal("malformed HLS must fail")
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
