package index_test

import (
	"path/filepath"
	"testing"

	idxpkg "media-archiver/internal/index"
	"media-archiver/internal/media"
)

func TestBuildAndLookupFrameAndTime(t *testing.T) {
	blocks := []media.CanonicalBlock{
		{Index: 0, OriginalOffset: 68, FrameCount: 240, StartFrame: 0, EndFrame: 240, StartNS: 0, EndNS: 5_000_000, PCM: make([]byte, 960)},
		{Index: 1, OriginalOffset: 1100, FrameCount: 240, StartFrame: 240, EndFrame: 480, StartNS: 5_000_000, EndNS: 10_000_000, PCM: make([]byte, 960)},
	}
	idx, err := idxpkg.Build(blocks, 48000, idxpkg.DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	if idx.TotalFrames != 480 || len(idx.Entries) != 2 {
		t.Fatalf("index=%+v", idx)
	}

	hit, err := idx.LookupFrame(300)
	if err != nil {
		t.Fatal(err)
	}
	if hit.Entry.BlockIndex != 1 || hit.FrameOffset != 60 {
		t.Fatalf("hit=%+v, want block1 offset60", hit)
	}
	if hit.BlockOriginalByte != 1100 {
		t.Fatalf("original byte=%d", hit.BlockOriginalByte)
	}

	timeHit, err := idx.LookupTimeNS(7_500_000) // frame 360 exactly
	if err != nil {
		t.Fatal(err)
	}
	if timeHit.Entry.BlockIndex != 1 || timeHit.FrameOffset != 120 {
		t.Fatalf("time hit=%+v", timeHit)
	}

	if _, err := idx.LookupFrame(480); err == nil {
		t.Fatal("expected end-exclusive lookup to fail")
	}
}

func TestIndexSaveLoadAndCorruptionValidation(t *testing.T) {
	blocks := []media.CanonicalBlock{{
		Index: 0, FrameCount: 240, StartFrame: 0, EndFrame: 240,
		StartNS: 0, EndNS: 5_000_000, PCM: make([]byte, 960),
	}}
	idx, err := idxpkg.Build(blocks, 48000, idxpkg.DefaultBuildOptions())
	if err != nil {
		t.Fatal(err)
	}
	data, err := idx.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "index.json")
	if err := media.WriteFileAtomic(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	loaded, err := idxpkg.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.TotalFrames != 240 {
		t.Fatalf("loaded total=%d", loaded.TotalFrames)
	}

	loaded.Entries[0].StartFrame = 5
	if err := loaded.Validate(); err == nil {
		t.Fatal("expected broken continuity validation error")
	}
}

func TestBuildRejectsExcessEntries(t *testing.T) {
	blocks := []media.CanonicalBlock{
		{Index: 0, FrameCount: 1, StartFrame: 0, EndFrame: 1},
		{Index: 1, FrameCount: 1, StartFrame: 1, EndFrame: 2},
	}
	if _, err := idxpkg.Build(blocks, 48000, idxpkg.BuildOptions{MaxEntries: 1}); err == nil {
		t.Fatal("expected entry memory limit rejection")
	}
	if _, err := idxpkg.Build(blocks, 48000, idxpkg.DefaultBuildOptions()); err != nil {
		t.Fatalf("default limit should accept fixture: %v", err)
	}
}
