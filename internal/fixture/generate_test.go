package fixture

import (
	"os"
	"path/filepath"
	"testing"
)

func specs() []TrackSpec {
	return []TrackSpec{
		{TrackID: 1, Timescale: 48000, SampleCount: 12, SampleSize: 20,
			SamplesPerChunk: 4, Delta: 480, SSRC: 111, PayloadType: 96},
	}
}

func TestBuildDeterministic(t *testing.T) {
	dir := t.TempDir()
	a, err := BuildMP4(filepath.Join(dir, "a"), 17, specs())
	if err != nil {
		t.Fatalf("build a: %v", err)
	}
	b, err := BuildMP4(filepath.Join(dir, "b"), 17, specs())
	if err != nil {
		t.Fatalf("build b: %v", err)
	}
	da, _ := os.ReadFile(a.Path)
	db, _ := os.ReadFile(b.Path)
	if string(da) != string(db) {
		t.Fatal("same seed must produce byte-identical files")
	}
}

func TestLayoutPointsAtEntries(t *testing.T) {
	dir := t.TempDir()
	f, err := BuildMP4(dir, 17, specs())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(f.Layout.StcoEntryOffset[0]) != 3 { // 12 samples / 4 per chunk
		t.Fatalf("chunks=%d want 3", len(f.Layout.StcoEntryOffset[0]))
	}
	off, err := MutateChunkOffset(f, 0, 1, 0xFFFFFFF0)
	if err != nil {
		t.Fatalf("mutate: %v", err)
	}
	if off != f.Layout.StcoEntryOffset[0][1] {
		t.Fatalf("mutated offset %d != layout %d", off, f.Layout.StcoEntryOffset[0][1])
	}
	if _, err := MutateSampleNumber(f, 0, 0, 9999); err != nil {
		t.Fatalf("mutate stss: %v", err)
	}
}

func TestTruncatePointInsideFile(t *testing.T) {
	dir := t.TempDir()
	f, err := BuildMP4(filepath.Join(dir, "src"), 17, specs())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	cut := f.TruncatePointInTable()
	if cut < 0 || cut >= f.Size {
		t.Fatalf("cut %d outside file size %d", cut, f.Size)
	}
	dst := filepath.Join(dir, "cut.mp4")
	if err := TruncateCopy(f.Path, dst, cut); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	info, err := os.Stat(dst)
	if err != nil || info.Size() != cut {
		t.Fatalf("truncated size wrong: %v", err)
	}
}
