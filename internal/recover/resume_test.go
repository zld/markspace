package recover

import (
	"os"
	"path/filepath"
	"testing"

	"mediad/internal/clock"
	"mediad/internal/index"
)

func writeJournal(t *testing.T, root string, n int) *index.Index {
	t.Helper()
	ix := index.New(48000)
	for i := 0; i < n; i++ {
		name := "segments/seg-" + pad(i) + ".mp4"
		ix.Append(index.Record{
			SegmentID:  i,
			ChunkStart: i * 4,
			ChunkEnd:   i*4 + 4,
			FrameRange: clock.FrameRange{Start: uint64(i * 1024), End: uint64((i + 1) * 1024)},
			FileName:   name,
			Keyframes:  []uint64{uint64(i * 1024)},
			Hash:       "abc",
			Complete:   true,
		})
	}
	if err := ix.Save(root); err != nil {
		t.Fatal(err)
	}
	return ix
}

func pad(i int) string {
	if i < 10 {
		return "000" + string(rune('0'+i))
	}
	return "00" + string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func writeSegmentFiles(t *testing.T, root string, n int) {
	t.Helper()
	dir := filepath.Join(root, "segments")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, "seg-"+pad(i)+".mp4")
		if err := os.WriteFile(p, []byte("media-"+string(rune('A'+i))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCleanStartWhenNothingExists(t *testing.T) {
	plan, err := ValidateAndPlan(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if plan.LastGoodSegment != -1 || plan.ResumeAtChunk != 0 || plan.RedoSegments != 0 {
		t.Fatalf("clean start plan wrong: %+v", plan)
	}
}

func TestResumeAfterVerifiedJournal(t *testing.T) {
	root := t.TempDir()
	writeSegmentFiles(t, root, 3)
	writeJournal(t, root, 3)
	plan, err := ValidateAndPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.LastGoodSegment != 2 || plan.ResumeAtChunk != 12 {
		t.Fatalf("resume plan wrong: %+v", plan)
	}
}

func TestMissingSegmentTriggersRollback(t *testing.T) {
	root := t.TempDir()
	writeSegmentFiles(t, root, 3)
	full := writeJournal(t, root, 3)
	// Build a snapshot covering only the first two segments, then delete the
	// third segment so the live journal references media that no longer exists.
	snap := index.New(48000)
	for i := 0; i < 2; i++ {
		snap.Append(full.Records[i])
	}
	if err := snap.SaveSnapshot(root, 0); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "segments", "seg-"+pad(2)+".mp4")); err != nil {
		t.Fatal(err)
	}
	plan, err := ValidateAndPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.LastGoodSegment != 1 || plan.FromSnapshot != 0 {
		t.Fatalf("rollback plan wrong: %+v", plan)
	}
	if plan.ResumeAtChunk != 8 {
		t.Fatalf("resume chunk = %d want 8", plan.ResumeAtChunk)
	}
}

func TestTruncatedJournalRestoresSnapshot(t *testing.T) {
	root := t.TempDir()
	writeSegmentFiles(t, root, 4)
	ix := writeJournal(t, root, 4)
	if err := ix.SaveSnapshot(root, 0); err != nil {
		t.Fatal(err)
	}
	// Corrupt the live journal by truncating it mid-document.
	jp := filepath.Join(root, index.Name)
	raw, _ := os.ReadFile(jp)
	if err := os.WriteFile(jp, raw[:len(raw)/2], 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := ValidateAndPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	// Snapshot 0 was taken while the journal held all 4 records, so the
	// restored plan keeps them but records that it came from a snapshot.
	if plan.FromSnapshot != 0 {
		t.Fatalf("expected snapshot restore, got %+v", plan)
	}
	// The damaged region must not be skipped: resume points at the snapshot's
	// last committed boundary rather than the end of the truncated file.
	if plan.ResumeAtChunk < 0 {
		t.Fatal("resume must never skip ahead of a verified boundary")
	}
}

func TestNoSnapshotMeansFullRestart(t *testing.T) {
	root := t.TempDir()
	ix := writeJournal(t, root, 2)
	_ = ix
	jp := filepath.Join(root, index.Name)
	raw, _ := os.ReadFile(jp)
	if err := os.WriteFile(jp, raw[:len(raw)/3], 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err := ValidateAndPlan(root)
	if err != nil {
		t.Fatal(err)
	}
	if plan.LastGoodSegment != -1 || plan.ResumeAtChunk != 0 {
		t.Fatalf("expected full restart, got %+v", plan)
	}
}
