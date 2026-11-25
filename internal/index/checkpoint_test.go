package index

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/clock"
	"mediad/internal/fault"
)

func sampleRecord(id int) Record {
	return Record{
		SegmentID:  id,
		ChunkStart: id * 4,
		ChunkEnd:   id*4 + 4,
		FrameRange: clock.FrameRange{Start: uint64(id * 1024), End: uint64((id + 1) * 1024)},
		FileName:   "seg-0001.mp4",
		Keyframes:  []uint64{uint64(id * 1024)},
		Tracks: []TrackIndex{
			{TrackID: 0, SourceChannel: "L", Bytes: 2048},
			{TrackID: 1, SourceChannel: "R", Bytes: 2048},
		},
		Hash:     "deadbeef",
		Complete: true,
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	root := t.TempDir()
	ix := New(48000)
	for i := 0; i < 5; i++ {
		ix.Append(sampleRecord(i))
	}
	if err := ix.Save(root); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if got.SampleRate != 48000 || got.LastGoodID != 4 || len(got.Records) != 5 {
		t.Fatalf("loaded index mismatch: %+v", got)
	}
	if got.Records[3].Tracks[1].SourceChannel != "R" {
		t.Fatal("nested track mapping not preserved")
	}
}

func TestLoadMissingReturnsNotFound(t *testing.T) {
	if _, err := Load(t.TempDir()); err != ErrNotFound {
		t.Fatalf("got %v want ErrNotFound", err)
	}
}

func TestTruncatedIndexIsCorruption(t *testing.T) {
	root := t.TempDir()
	ix := New(48000)
	ix.Append(sampleRecord(0))
	if err := ix.Save(root); err != nil {
		t.Fatal(err)
	}
	p, _ := path(root)
	raw, _ := os.ReadFile(p)
	if err := os.WriteFile(p, raw[:len(raw)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(root)
	fe, ok := fault.As(err)
	if !ok || fe.Category != fault.CatCheckpoint {
		t.Fatalf("expected checkpoint corruption, got %v", err)
	}
}

func TestInconsistentRecordsRejected(t *testing.T) {
	root := t.TempDir()
	ix := New(48000)
	ix.Append(sampleRecord(0))
	ix.Records = append(ix.Records, sampleRecord(2)) // gap in segment ids
	ix.LastGoodID = 2
	data, _ := jsonMarshal(ix)
	dst, _ := path(root)
	if err := safeioAtomic(dst, data); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root); err == nil {
		t.Fatal("non-contiguous index was accepted")
	}
}

func TestSnapshotSaveAndLatest(t *testing.T) {
	root := t.TempDir()
	ix := New(48000)
	for i := 0; i < 3; i++ {
		ix.Append(sampleRecord(i))
	}
	if err := ix.SaveSnapshot(root, 0); err != nil {
		t.Fatal(err)
	}
	ix.Append(sampleRecord(3))
	if err := ix.SaveSnapshot(root, 1); err != nil {
		t.Fatal(err)
	}
	n, p, err := LatestSnapshot(root)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || filepath.Base(p) != "checkpoint-000001.json" {
		t.Fatalf("latest snapshot wrong: n=%d path=%s", n, p)
	}
}

func TestLatestSnapshotMissing(t *testing.T) {
	if _, _, err := LatestSnapshot(t.TempDir()); err != ErrNotFound {
		t.Fatalf("got %v want ErrNotFound", err)
	}
}
