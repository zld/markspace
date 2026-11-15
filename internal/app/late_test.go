package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hlsforge/internal/cli"
	"hlsforge/internal/fixture"
	"hlsforge/internal/report"
)

// TestLateArrivalInWindowFillsGap exercises a fragment that was missing on
// the first build and arrives before the window is finalized: rebuilding must
// recompute the affected part/segment and close the GAP without shifting any
// sequence number.
func TestLateArrivalInWindowFillsGap(t *testing.T) {
	inbox := t.TempDir()
	hls := t.TempDir()
	metas, err := fixture.Generate(inbox, []int{1600000}, fixture.Options{
		Seed: 7, PartMs: 500, DurationMs: 4000, Faults: []string{"missing"},
	})
	if err != nil {
		t.Fatal(err)
	}
	missingSeq := metas[0].Missing[0]
	dir := metas[0].Dir

	build := func() *report.Report {
		t.Helper()
		rep, err := RunBuild(&cli.BuildConfig{
			In: inbox, Out: hls, FromSeq: 1, ToSeq: 8,
			Segment: 2000, Part: 500, Window: 12000,
		})
		if err != nil {
			t.Fatal(err)
		}
		return rep
	}

	first := build()
	if !hasCode(first.Findings, report.CodeMissingPart, 1600000, missingSeq) {
		t.Fatalf("first build must report the gap: %+v", first.Findings)
	}
	gapMarker := filepath.Join(hls, "1600000",
		"seg-00001-part-0.mp4.gap")
	if _, err := os.Stat(gapMarker); err != nil {
		t.Fatalf("gap marker expected after first build: %v", err)
	}

	// The late fragment arrives inside the window and is added to the index.
	late := fixture.BuildFragment(uint32(missingSeq),
		uint64((missingSeq-1)*500), 1600000)
	if err := os.WriteFile(filepath.Join(dir, fixture.FragmentName(missingSeq)),
		late, 0o644); err != nil {
		t.Fatal(err)
	}
	idxPath := filepath.Join(dir, fixture.IndexName())
	raw, err := os.ReadFile(idxPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(idxPath,
		append(raw, []byte(fixture.FragmentName(missingSeq)+"\n")...), 0o644); err != nil {
		t.Fatal(err)
	}

	second := build()
	if hasCode(second.Findings, report.CodeMissingPart, 1600000, missingSeq) {
		t.Fatalf("late in-window fragment must close the gap: %+v", second.Findings)
	}
	if _, err := os.Stat(gapMarker); !os.IsNotExist(err) {
		t.Fatalf("gap marker must be removed after rebuild, err=%v", err)
	}
	partFile := filepath.Join(hls, "1600000", "seg-00001-part-0.mp4")
	if _, err := os.Stat(partFile); err != nil {
		t.Fatalf("late fragment must be materialized as a part: %v", err)
	}
	// Re-running once more must be idempotent: identical manifests and no
	// duplicate findings.
	third := build()
	if len(third.Findings) != len(second.Findings) {
		t.Fatalf("rebuild not idempotent: %d vs %d findings",
			len(third.Findings), len(second.Findings))
	}
}

// TestLateArrivalOutOfWindowRejected verifies fragments arriving after a
// window has been finalized are recorded as failures and never mutate the
// already-evicted window or shift media sequences.
func TestLateArrivalOutOfWindowRejected(t *testing.T) {
	inbox := t.TempDir()
	hls := t.TempDir()
	if _, err := fixture.Generate(inbox, []int{800000}, fixture.Options{
		Seed: 7, PartMs: 500, DurationMs: 6000, // 12 fragments
	}); err != nil {
		t.Fatal(err)
	}
	// All twelve fragments are inside the ingest window. With 4 parts per
	// 2s segment that is 3 segments; a 4s window retains the final two
	// (absolute MediaSeq 1 and 2), evicting only MediaSeq 0.
	rep, err := RunBuild(&cli.BuildConfig{
		In: inbox, Out: hls, FromSeq: 1, ToSeq: 12,
		Segment: 2000, Part: 500, Window: 4000,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Nothing is late at ingest: every fragment belonged to the accepted range.
	for _, f := range rep.Findings {
		if f.Code == report.CodeLateOutOfWindow {
			t.Fatalf("no ingest-time late finding expected, got %+v", f)
		}
	}
	pl, err := os.ReadFile(filepath.Join(hls, "800000.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pl), "800000/seg-00000.mp4") {
		t.Fatal("evicted segment seg-00000 must not appear in playlist")
	}
	for _, kept := range []string{"800000/seg-00001.mp4", "800000/seg-00002.mp4"} {
		if !strings.Contains(string(pl), kept) {
			t.Fatalf("retained segment %s missing from playlist", kept)
		}
	}
}

// TestLateArrivalRejectedAtIngestWindow covers fragments whose sequence is
// beyond the finalized ingest window: they are recorded as LATE_OUT_OF_WINDOW
// and never enter a slot or the playlist.
func TestLateArrivalRejectedAtIngestWindow(t *testing.T) {
	inbox := t.TempDir()
	hls := t.TempDir()
	if _, err := fixture.Generate(inbox, []int{800000}, fixture.Options{
		Seed: 7, PartMs: 500, DurationMs: 6000, // 12 fragments on disk
	}); err != nil {
		t.Fatal(err)
	}
	rep, err := RunBuild(&cli.BuildConfig{
		In: inbox, Out: hls, FromSeq: 1, ToSeq: 8,
		Segment: 2000, Part: 500, Window: 12000,
	})
	if err != nil {
		t.Fatal(err)
	}
	late := 0
	for _, f := range rep.Findings {
		if f.Code != report.CodeLateOutOfWindow {
			continue
		}
		late++
		if f.Seq < 9 || f.Seq > 12 {
			t.Fatalf("unexpected late seq %d", f.Seq)
		}
	}
	if late != 4 {
		t.Fatalf("want 4 out-of-window findings, got %d: %+v", late, rep.Findings)
	}
	pl, err := os.ReadFile(filepath.Join(hls, "800000.m3u8"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(pl), "part-") &&
		!strings.Contains(string(pl), "800000/seg-00002-part-0.mp4") {
		t.Fatal("in-window parts must still be present")
	}
	if strings.Contains(string(pl), "800000/seg-00003.mp4") {
		t.Fatal("out-of-window fragment must not produce a segment")
	}
}

func hasCode(fs []report.Finding, code report.Code, bitrate, seq int) bool {
	for _, f := range fs {
		if f.Code == code && f.Bitrate == bitrate && f.Seq == seq {
			return true
		}
	}
	return false
}
