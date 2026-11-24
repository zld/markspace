package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"archivecheck/internal/media"
	"archivecheck/internal/merr"
	"archivecheck/internal/timeline"
)

func buildTimeline() *timeline.Timeline {
	mk := func(seq, idx uint32, pts int64, luma uint8, corrupt bool) media.Frame {
		return media.Frame{
			TrackID: 1, SegmentSeq: seq, Index: idx, PTS: pts,
			Duration: 3000, Width: 160, Height: 90, Luma: luma, Corrupt: corrupt,
		}
	}
	return &timeline.Timeline{
		Segments: []*media.Segment{
			{TrackID: 1, Seq: 1, StartTicks: 0, DurationTicks: 180000,
				Frames: []media.Frame{mk(1, 0, 0, 100, false), mk(1, 1, 3000, 110, false)}},
			{TrackID: 1, Seq: 2, StartTicks: 180000, DurationTicks: 180000,
				Frames: []media.Frame{mk(2, 0, 180000, 120, false)}},
		},
	}
}

func TestExportDeterministic(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	m1, err := Export(buildTimeline(), filepath.Join(dir1, "frames"))
	if err != nil {
		t.Fatal(err)
	}
	m2, err := Export(buildTimeline(), filepath.Join(dir2, "frames"))
	if err != nil {
		t.Fatal(err)
	}
	if m1.Count != 3 || len(m1.Hashes()) != 3 {
		t.Fatalf("frame count = %d", m1.Count)
	}
	for i := range m1.Frames {
		if m1.Frames[i].Hash != m2.Frames[i].Hash {
			t.Fatalf("hash mismatch at %d: %s vs %s", i, m1.Frames[i].Hash, m2.Frames[i].Hash)
		}
		if m1.Frames[i].Timestamp != m2.Frames[i].Timestamp {
			t.Fatalf("timestamp mismatch at %d", i)
		}
	}
	// File must exist and parse back identically (atomic commit).
	data, err := os.ReadFile(filepath.Join(dir1, "frames", "frames.json"))
	if err != nil {
		t.Fatal(err)
	}
	var onDisk Manifest
	if err := json.Unmarshal(data, &onDisk); err != nil {
		t.Fatal(err)
	}
	if onDisk.Count != m1.Count || onDisk.Frames[2].Hash != m1.Frames[2].Hash {
		t.Fatal("on-disk manifest differs from in-memory result")
	}
}

func TestExportIdempotentRerun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	first, err := Export(buildTimeline(), dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Export(buildTimeline(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Count != second.Count {
		t.Fatal("rerun produced a different frame count")
	}
	for i := range first.Hashes() {
		if first.Hashes()[i] != second.Hashes()[i] {
			t.Fatal("rerun produced different hashes")
		}
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".tmp" {
			t.Fatal("temporary artifact left behind after atomic commit")
		}
	}
}

func TestExportRejectsTraversal(t *testing.T) {
	_, err := Export(buildTimeline(), "../escape")
	if err == nil {
		t.Fatal("traversal output path must be rejected")
	}
	me, ok := merr.As(err)
	if !ok || me.Kind != merr.KindArgument {
		t.Fatalf("wrong error: %v", err)
	}
}

func TestExportRejectsFramelessRaster(t *testing.T) {
	tl := buildTimeline()
	tl.Segments[0].Frames[0].Width = 0
	_, err := Export(tl, filepath.Join(t.TempDir(), "out"))
	if err == nil {
		t.Fatal("frame without dimensions must be rejected as unsupported")
	}
}
