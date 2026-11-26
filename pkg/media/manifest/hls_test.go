package manifest

import (
	"bytes"
	"strings"
	"testing"

	"cmafrepair/pkg/media/cmaf"
)

func timelineWithGap() *cmaf.Timeline {
	return &cmaf.Timeline{
		Representation: "video_90000",
		Timescale:      90000,
		Entries: []cmaf.TimelineEntry{
			{Name: "f1.m4s", Sequence: 1, Status: cmaf.StatusIntact, StartMs: 0, EndMs: 2000},
			{Name: "f2.m4s", Sequence: 2, Status: cmaf.StatusGap, StartMs: 2000, EndMs: 2000},
			{Name: "f3.m4s", Sequence: 3, Status: cmaf.StatusIntact, StartMs: 2000, EndMs: 4000},
		},
	}
}

func TestFromTimelineMarksGapAndDiscontinuity(t *testing.T) {
	pl := FromTimeline(timelineWithGap())
	if pl.GapCount() != 1 {
		t.Fatalf("gap count=%d want 1", pl.GapCount())
	}
	if !pl.Segments[1].Gap || pl.Segments[1].URI != "" {
		t.Fatalf("gap segment wrong: %+v", pl.Segments[1])
	}
	if !pl.Segments[2].Discontinuity {
		t.Fatal("first segment after a GAP must carry EXT-X-DISCONTINUITY")
	}
	if pl.TargetDuration != 2 {
		t.Fatalf("target duration=%d want 2", pl.TargetDuration)
	}
}

func TestWriteMediaPlaylistRendersGapTag(t *testing.T) {
	pl := FromTimeline(timelineWithGap())
	var buf bytes.Buffer
	if err := WriteMediaPlaylist(&buf, pl, 1); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "#EXT-X-GAP") {
		t.Fatalf("playlist missing EXT-X-GAP:\n%s", out)
	}
	if !strings.Contains(out, "#EXT-X-DISCONTINUITY") {
		t.Fatalf("playlist missing DISCONTINUITY:\n%s", out)
	}
	if strings.Count(out, "f1.m4s") != 1 || strings.Count(out, "f3.m4s") != 1 {
		t.Fatalf("good segments must still be listed:\n%s", out)
	}
	if !strings.HasPrefix(out, "#EXTM3U") || !strings.Contains(out, "#EXT-X-ENDLIST") {
		t.Fatal("playlist framing wrong")
	}
}

func TestWriteMasterPlaylist(t *testing.T) {
	var buf bytes.Buffer
	vars := []Variant{
		{Name: "v720", Bandwidth: 2000000, Resolution: "1280x720", Codecs: "avc1.64001f"},
		{Name: "v360", Bandwidth: 800000, Resolution: "640x360", Codecs: "avc1.4d401e"},
	}
	if err := WriteMasterPlaylist(&buf, vars); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "v360.m3u8") || !strings.Contains(out, "v720.m3u8") {
		t.Fatalf("variants missing:\n%s", out)
	}
}

func TestFingerprintChangesOnMetadataOnly(t *testing.T) {
	pl := FromTimeline(timelineWithGap())
	fp1 := pl.FingerprintView()
	// Simulate a metadata-only change: bump target duration, same segments.
	pl.TargetDuration = 5
	fp2 := pl.FingerprintView()
	if fp1.TargetDuration == fp2.TargetDuration {
		t.Fatal("test setup: target duration did not change")
	}
	// Segment URIs/gaps are unchanged, so only the duration metadata moved.
	if len(fp1.Segments) != len(fp2.Segments) {
		t.Fatal("metadata-only change must not alter segment set")
	}
}
