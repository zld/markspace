package ingest

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	"hlsforge/internal/fixture"
	"hlsforge/internal/report"
)

func hasCode(fs []report.Finding, code report.Code, bitrate, seq int) bool {
	for _, f := range fs {
		if f.Code == code && f.Bitrate == bitrate && f.Seq == seq {
			return true
		}
	}
	return false
}

func countCode(fs []report.Finding, code report.Code) int {
	n := 0
	for _, f := range fs {
		if f.Code == code {
			n++
		}
	}
	return n
}

func TestCollectHappyPathWithReorderAndDuplicate(t *testing.T) {
	root := t.TempDir()
	metas, err := fixture.Generate(root, []int{800000}, fixture.Options{
		Seed: 3, PartMs: 500, DurationMs: 2000, Faults: []string{"order"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(metas[0].Delivered) != 5 { // 4 fragments + 1 duplicate
		t.Fatalf("delivery lines = %d", len(metas[0].Delivered))
	}
	res, err := Collect(root, 1, 4, 500)
	if err != nil {
		t.Fatal(err)
	}
	track := res.Tracks[0]
	if len(track.Present()) != 4 {
		t.Fatalf("present = %d, want 4", len(track.Present()))
	}
	if len(track.Missing()) != 0 || countCode(res.Findings, report.CodeMissingPart) != 0 {
		t.Fatalf("duplicate must be idempotent: %+v", res.Findings)
	}
}

func TestCollectMissingSlots(t *testing.T) {
	root := t.TempDir()
	metas, err := fixture.Generate(root, []int{800000, 1600000}, fixture.Options{
		Seed: 1, PartMs: 500, DurationMs: 2000, Faults: []string{"missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// The missing fault targets the second bitrate; its mid-window seq is 3.
	missingSeq := metas[1].Missing[0]
	res, err := Collect(root, 1, 4, 500)
	if err != nil {
		t.Fatal(err)
	}
	hi := findTrack(t, res, 1600000)
	lo := findTrack(t, res, 800000)
	if got := hi.Missing(); len(got) != 1 || got[0] != missingSeq {
		t.Fatalf("missing slots = %v", got)
	}
	if !hasCode(res.Findings, report.CodeMissingPart, 1600000, missingSeq) {
		t.Fatalf("missing finding absent: %+v", res.Findings)
	}
	// Present slots must not be shifted: the slot after the gap stays nil-safe.
	if hi.Slots[missingSeq] != nil || hi.Slots[4] == nil {
		t.Fatal("gap must retain an explicit nil slot without shifting later seqs")
	}
	if len(lo.Missing()) != 0 {
		t.Fatal("first bitrate must be complete")
	}
}

func findTrack(t *testing.T, res *Result, bitrate int) *TrackIngest {
	t.Helper()
	for _, tr := range res.Tracks {
		if tr.Bitrate == bitrate {
			return tr
		}
	}
	t.Fatalf("track %d not found", bitrate)
	return nil
}

func TestCollectOutOfWindowLate(t *testing.T) {
	root := t.TempDir()
	_, err := fixture.Generate(root, []int{800000}, fixture.Options{
		Seed: 1, PartMs: 500, DurationMs: 2500, // 5 fragments
	})
	if err != nil {
		t.Fatal(err)
	}
	// Window only covers seqs 1..4; fragment 5 is a late out-of-window arrival.
	res, err := Collect(root, 1, 4, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Findings, report.CodeLateOutOfWindow, 800000, 5) {
		t.Fatalf("late finding absent: %+v", res.Findings)
	}
	if _, ok := res.Tracks[0].Slots[5]; ok {
		t.Fatal("out-of-window fragment must not enter the collection")
	}
}

func TestCollectConflict(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "800000")
	if _, err := fixture.Generate(root, []int{800000}, fixture.Options{
		Seed: 1, PartMs: 500, DurationMs: 1000,
	}); err != nil {
		t.Fatal(err)
	}
	// Deliver two distinct, structurally valid fragments both carrying
	// mfhd sequence 1: the original plus a separately synthesized one.
	conflict := fixture.BuildFragment(1, 0, 999999)
	if err := os.WriteFile(filepath.Join(dir, "frag-00001b.mp4"), conflict, 0o644); err != nil {
		t.Fatal(err)
	}
	idx := "frag-00001.mp4\nfrag-00001b.mp4\nfrag-00002.mp4\n"
	if err := os.WriteFile(filepath.Join(dir, fixture.IndexName()), []byte(idx), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := Collect(root, 1, 2, 500)
	if err != nil {
		t.Fatal(err)
	}
	if !hasCode(res.Findings, report.CodeConflict, 800000, 1) {
		t.Fatalf("conflict finding absent: %+v", res.Findings)
	}
	// The first delivery wins; later conflicting bytes must not replace it.
	if res.Tracks[0].Slots[1] == nil {
		t.Fatal("first delivery of seq 1 must be retained")
	}
	if got := res.Tracks[0].Slots[1].SHA256; got == sha256.Sum256(conflict) {
		t.Fatal("conflicting re-delivery must not overwrite the stored fragment")
	}
}
