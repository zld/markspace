package fixture

import (
	"os"
	"path/filepath"
	"testing"

	"transcode/internal/media"
)

func TestGenerateDeterministicAndEdgeFrames(t *testing.T) {
	dir := t.TempDir()
	spec := Spec{
		Path:        filepath.Join(dir, "sample.vid"),
		SidecarPath: filepath.Join(dir, "samples.txt"),
		DurationSec: 4, FPS: 10, Width: 8, Height: 6,
	}
	v, samples, err := Generate(spec)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Frames) != 40 || len(samples) != 40 {
		t.Fatalf("frame/sample count mismatch: %d vs %d", len(v.Frames), len(samples))
	}
	// Re-run and expect byte-identical output.
	v2, samples2, err := Generate(spec)
	if err != nil {
		t.Fatal(err)
	}
	for i := range v2.Frames {
		if string(v.Frames[i]) != string(v2.Frames[i]) || samples[i] != samples2[i] {
			t.Fatalf("nondeterministic output at frame %d", i)
		}
	}
	// Pinned black frame.
	blackAt := len(v.Frames) / 4
	for _, b := range v.Frames[blackAt] {
		if b != 0 {
			t.Fatal("expected an all-zero black frame")
		}
	}
	// Pinned identical frame pair.
	dupAt := len(v.Frames) / 2
	if string(v.Frames[dupAt]) != string(v.Frames[dupAt-1]) {
		t.Fatal("expected duplicated frame pair")
	}
	// On-disk round trip plus sidecar reload.
	if _, err := media.ReadVideo(spec.Path); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSamples(spec.SidecarPath)
	if err != nil || len(loaded) != len(samples) || loaded[0] != samples[0] {
		t.Fatalf("sidecar reload failed: %v", err)
	}
}

func TestGenerateRejectsInvalidSpec(t *testing.T) {
	if _, _, err := (Generate(Spec{DurationSec: 0})); err == nil {
		t.Fatal("expected validation error")
	}
	if _, err := LoadSamples(filepath.Join(t.TempDir(), "missing.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected not-exist, got %v", err)
	}
}
