package segment

import (
	"errors"
	"testing"

	"hlsforge/internal/ingest"
	"hlsforge/internal/timeline"
)

func buildTrack(t *testing.T, missing ...int) *timeline.Track {
	t.Helper()
	slots := map[int]*ingest.Fragment{}
	for seq := 1; seq <= 8; seq++ {
		gap := false
		for _, m := range missing {
			if m == seq {
				gap = true
			}
		}
		if !gap {
			slots[seq] = &ingest.Fragment{
				Bitrate: 800000, Seq: seq,
				BaseTime: uint64((seq - 1) * 500), Duration: 500, Sync: true,
			}
		}
	}
	in := &ingest.TrackIngest{
		Bitrate: 800000, Timescale: 1000, PartMs: 500,
		FromSeq: 1, ToSeq: 8, Slots: slots,
	}
	tr, err := timeline.Build(in)
	if err != nil {
		t.Fatal(err)
	}
	return tr
}

func TestLayoutSegmentsAndParts(t *testing.T) {
	tr := buildTrack(t)
	ls, err := Layout(tr, 2000, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(ls.Segments))
	}
	for i, seg := range ls.Segments {
		if len(seg.Parts) != 4 || seg.MediaSeq != i || seg.DurationMs != 2000 {
			t.Fatalf("segment %d malformed: %+v", i, seg)
		}
		for j, p := range seg.Parts {
			if p.Index != j || p.Gap || !p.Independent {
				t.Fatalf("part %d/%d malformed: %+v", i, j, p)
			}
		}
	}
	if ls.PartCount() != 8 || ls.GapPartCount() != 0 {
		t.Fatalf("part counts wrong: %d gaps %d", ls.PartCount(), ls.GapPartCount())
	}
}

func TestLayoutRetainsGapParts(t *testing.T) {
	// Gap at seq 4 (last part of segment 0): next segment still starts on a
	// filled sync sample, so this must remain valid.
	tr := buildTrack(t, 4)
	ls, err := Layout(tr, 2000, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ls.Segments[0].HasGap || ls.Segments[0].HasContent == false {
		t.Fatal("segment 0 should contain both content and a gap part")
	}
	gap := ls.Segments[0].Parts[3]
	if !gap.Gap || gap.PartSeq != 4 {
		t.Fatalf("gap part wrong: %+v", gap)
	}
	if ls.GapPartCount() != 1 {
		t.Fatalf("gap count = %d", ls.GapPartCount())
	}
}

func TestLayoutBoundaryInsideGapIsRetained(t *testing.T) {
	// Gap at seq 5 means segment 1 begins in a gap: the slot is retained as an
	// explicit GAP, later sequences are not shifted forward.
	tr := buildTrack(t, 5)
	ls, err := Layout(tr, 2000, 500, 0)
	if err != nil {
		t.Fatalf("leading-gap segment must be retained, got %v", err)
	}
	seg1 := ls.Segments[1]
	if !seg1.HasGap || seg1.Parts[0].PartSeq != 5 || !seg1.Parts[0].Gap {
		t.Fatalf("segment 1 must start with a gap part: %+v", seg1.Parts[0])
	}
	if seg1.Parts[1].PartSeq != 6 || seg1.Parts[1].Gap {
		t.Fatalf("later parts must keep their sequence numbers: %+v", seg1.Parts)
	}
}

func TestLayoutRejectsBoundaryOnNonSyncFragment(t *testing.T) {
	tr := buildTrack(t)
	// Mark the fragment opening segment 1 as a non-sync sample.
	tr.Parts[4].Fragment.Sync = false
	if _, err := Layout(tr, 2000, 500, 0); !errors.Is(err, ErrBoundaryNotSync) {
		t.Fatalf("want ErrBoundaryNotSync, got %v", err)
	}
}

func TestLayoutValidation(t *testing.T) {
	tr := buildTrack(t)
	if _, err := Layout(tr, 700, 500, 0); err == nil {
		t.Fatal("non-multiple segment must error")
	}
	if _, err := Layout(tr, 0, 500, 0); err == nil {
		t.Fatal("zero segment must error")
	}
}

func TestLayoutURIsStable(t *testing.T) {
	tr := buildTrack(t)
	ls, err := Layout(tr, 2000, 500, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantSeg := "800000/seg-00001.mp4"
	if ls.Segments[1].URI != wantSeg {
		t.Fatalf("segment uri = %q, want %q", ls.Segments[1].URI, wantSeg)
	}
	wantPart := "800000/seg-00001-part-2.mp4"
	if ls.Segments[1].Parts[2].URI != wantPart {
		t.Fatalf("part uri = %q, want %q", ls.Segments[1].Parts[2].URI, wantPart)
	}
	if ls.MediaPlaylist != "800000.m3u8" {
		t.Fatalf("playlist name = %q", ls.MediaPlaylist)
	}
}

func TestLayoutRollingWindowKeepsAbsoluteSequence(t *testing.T) {
	// 8 parts over 2s segments = 2 full segments; a 2s window keeps only the
	// last segment while preserving its absolute MediaSeq and URIs.
	tr := buildTrack(t)
	if len(tr.Parts) != 8 {
		t.Fatalf("parts = %d", len(tr.Parts))
	}
	ls, err := Layout(tr, 2000, 500, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls.Segments) != 1 {
		t.Fatalf("windowed segments = %d, want 1", len(ls.Segments))
	}
	seg := ls.Segments[0]
	if seg.MediaSeq != 1 {
		t.Fatalf("MediaSeq must stay absolute: got %d", seg.MediaSeq)
	}
	if seg.URI != "800000/seg-00001.mp4" {
		t.Fatalf("URI must not be renumbered: %s", seg.URI)
	}
	if seg.Parts[0].PartSeq != 5 {
		t.Fatalf("first retained part seq = %d, want 5", seg.Parts[0].PartSeq)
	}
}

func TestLayoutWindowLargerThanTimelineKeepsAll(t *testing.T) {
	tr := buildTrack(t)
	ls, err := Layout(tr, 2000, 500, 12000)
	if err != nil {
		t.Fatal(err)
	}
	if len(ls.Segments) != 2 || ls.Segments[0].MediaSeq != 0 {
		t.Fatalf("oversized window must keep every segment: %+v", ls.Segments)
	}
}

func TestLayoutRejectsWindowAboveCap(t *testing.T) {
	tr := buildTrack(t)
	if _, err := Layout(tr, 2000, 500, MaxWindowMs+1); err == nil {
		t.Fatal("window above cap must be rejected")
	}
}
