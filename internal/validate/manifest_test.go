package validate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"streamcheck/internal/manifest"
)

func TestVerifyManifest(t *testing.T) {
	dir := t.TempDir()
	segs := []manifest.Segment{
		{ID: 2, RateKbps: 800, FileName: "seg_800_2.ts", Path: filepath.Join(dir, "seg_800_2.ts"), DurationMS: 4000},
		{ID: 0, RateKbps: 800, FileName: "seg_800_0.ts", Path: filepath.Join(dir, "seg_800_0.ts"), DurationMS: 4000},
		{ID: 1, RateKbps: 800, FileName: "seg_800_1.ts", Path: filepath.Join(dir, "seg_800_1.ts"), DurationMS: 4000},
	}
	for _, s := range segs {
		if err := os.WriteFile(s.Path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	p, err := manifest.BuildPlaylist(800, segs)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "media_800.m3u8")
	if err := os.WriteFile(path, manifest.Render(p), 0o644); err != nil {
		t.Fatal(err)
	}
	digest, issues := VerifyManifest(path, p.Segments, dir)
	if len(issues) != 0 {
		t.Fatalf("issues = %v", issues)
	}
	if len(digest) != 64 {
		t.Fatalf("digest = %q", digest)
	}

	if err := os.Remove(filepath.Join(dir, "seg_800_1.ts")); err != nil {
		t.Fatal(err)
	}
	_, issues = VerifyManifest(path, p.Segments, dir)
	if len(issues) != 1 || !strings.Contains(issues[0], "manifest_missing_file") {
		t.Fatalf("issues = %v", issues)
	}
}
