package segment

import (
	"os"
	"path/filepath"
	"testing"

	"hlsforge/internal/fixture"
	"hlsforge/internal/ingest"
	"hlsforge/internal/timeline"
)

func TestPersistWritesPartsSegmentsAndGapMarkers(t *testing.T) {
	inbox := t.TempDir()
	out := t.TempDir()
	metas, err := fixture.Generate(inbox, []int{800000}, fixture.Options{
		Seed: 5, PartMs: 500, DurationMs: 4000,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Drop fragment 4 (last part of segment 0) from the inbox before ingest.
	gapName := fixture.FragmentName(4)
	if err := os.Remove(filepath.Join(metas[0].Dir, gapName)); err != nil {
		t.Fatal(err)
	}
	idx := "frag-00001.mp4\nfrag-00002.mp4\nfrag-00003.mp4\n" +
		"frag-00005.mp4\nfrag-00006.mp4\nfrag-00007.mp4\nfrag-00008.mp4\n"
	if err := os.WriteFile(filepath.Join(metas[0].Dir, fixture.IndexName()),
		[]byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ingest.Collect(inbox, 1, 8, 500)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := timeline.Build(res.Tracks[0])
	if err != nil {
		t.Fatal(err)
	}
	layout, err := Layout(tr, 2000, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	written, err := Persist(out, tr, layout)
	if err != nil {
		t.Fatal(err)
	}
	// 7 content parts + 1 gap part marker + 2 segment files (both have content).
	if len(written) != 10 {
		t.Fatalf("written files = %d: %+v", len(written), written)
	}
	gapPath := filepath.Join(out, "800000", "seg-00000-part-3.mp4.gap")
	if raw, err := os.ReadFile(gapPath); err != nil || string(raw) != GapMarker {
		t.Fatalf("gap marker missing or wrong: %v %q", err, raw)
	}
	if _, err := os.Stat(filepath.Join(out, "800000", "seg-00000.mp4")); err != nil {
		t.Fatalf("segment 0 media file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "800000", "seg-00001.mp4")); err != nil {
		t.Fatalf("segment 1 media file missing: %v", err)
	}
	// No temp files may survive the atomic renames.
	entries, err := os.ReadDir(filepath.Join(out, "800000"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if filepath.Ext(e.Name()) == "" && len(e.Name()) > 4 && e.Name()[:4] == ".tmp" {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

func TestPersistAllGapSegmentKeepsSlot(t *testing.T) {
	inbox := t.TempDir()
	out := t.TempDir()
	metas, err := fixture.Generate(inbox, []int{800000}, fixture.Options{
		Seed: 5, PartMs: 500, DurationMs: 4000,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Remove the whole first segment (seqs 1..4): boundary is the initial
	// boundary, which layout permits; the segment slot becomes a GAP.
	for seq := 1; seq <= 4; seq++ {
		if err := os.Remove(filepath.Join(metas[0].Dir, fixture.FragmentName(seq))); err != nil {
			t.Fatal(err)
		}
	}
	idx := "frag-00005.mp4\nfrag-00006.mp4\nfrag-00007.mp4\nfrag-00008.mp4\n"
	if err := os.WriteFile(filepath.Join(metas[0].Dir, fixture.IndexName()),
		[]byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := ingest.Collect(inbox, 1, 8, 500)
	if err != nil {
		t.Fatal(err)
	}
	tr, err := timeline.Build(res.Tracks[0])
	if err != nil {
		t.Fatal(err)
	}
	layout, err := Layout(tr, 2000, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Persist(out, tr, layout); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(out, "800000", "seg-00000.mp4.gap")); err != nil {
		t.Fatalf("all-gap segment must keep its slot via marker: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "800000", "seg-00001.mp4")); err != nil {
		t.Fatalf("later segment must still be written: %v", err)
	}
}
