package damage

import (
	"testing"

	"continuity/internal/timeline"
)

func TestMergeOverlapping(t *testing.T) {
	rs := []timeline.Range{
		{Track: "audio/rtp", Reason: "packet gap", StartSeq: 10, EndSeq: 12, Start: 9600, End: 12480},
		{Track: "audio/rtp", Reason: "truncated packet", StartSeq: 12, EndSeq: 12, Start: 12000, End: 13000},
		{Track: "audio/rtp", Reason: "packet gap", StartSeq: 40, EndSeq: 41, Start: 38400, End: 40320},
		{Track: "video/mp4#2", Reason: "keyframe interval too long", Start: 0, End: 90000},
	}
	out := Merge(rs)
	if len(out) != 3 {
		t.Fatalf("got %d ranges: %+v", len(out), out)
	}
	m := out[0]
	if m.Start != 9600 || m.End != 13000 || m.StartSeq != 10 || m.EndSeq != 12 {
		t.Fatalf("merged range: %+v", m)
	}
	if m.Reason != "packet gap;truncated packet" {
		t.Fatalf("reasons: %q", m.Reason)
	}
	if out[1].Track != "audio/rtp" || out[2].Track != "video/mp4#2" {
		t.Fatalf("track grouping wrong: %+v", out)
	}
}

func TestMergeEmpty(t *testing.T) {
	if Merge(nil) != nil {
		t.Fatal("expected nil")
	}
}
