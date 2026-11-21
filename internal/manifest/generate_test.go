package manifest

import (
	"strings"
	"testing"
)

func sampleSegments() []Segment {
	return []Segment{
		{ID: 2, RateKbps: 800, FileName: "seg_800_2.ts", Path: "seg_800_2.ts", DurationMS: 4001, SizeBytes: 10},
		{ID: 0, RateKbps: 800, FileName: "seg_800_0.ts", Path: "seg_800_0.ts", DurationMS: 4000, SizeBytes: 10},
		{ID: 1, RateKbps: 800, FileName: "seg_800_1.ts", Path: "seg_800_1.ts", DurationMS: 3999, SizeBytes: 10},
		{ID: 5, RateKbps: 1600, FileName: "seg_1600_5.ts", Path: "seg_1600_5.ts", DurationMS: 4000, SizeBytes: 20},
		{ID: 4, RateKbps: 1600, FileName: "seg_1600_4.ts", Path: "seg_1600_4.ts", DurationMS: 4000, SizeBytes: 20},
	}
}

func TestBuildPlaylistSkipsUnusableAndEmpty(t *testing.T) {
	segs := []Segment{
		{ID: 0, RateKbps: 800, FileName: "a", DurationMS: 0},
		{ID: 1, RateKbps: 800, FileName: "b", DurationMS: 4000, ProbeError: "timeout"},
	}
	p, err := BuildPlaylist(800, segs)
	if err != nil {
		t.Fatalf("BuildPlaylist: %v", err)
	}
	if len(p.Segments) != 0 || p.MediaSequence != 0 || p.TargetDuration != 1 {
		t.Fatalf("empty playlist = %+v", p)
	}
	if !strings.Contains(string(Render(p)), "#EXT-X-TARGETDURATION:1\n") {
		t.Fatal("empty playlist target duration")
	}
}

func TestBuildPlaylistRejectsDuplicateIDs(t *testing.T) {
	segs := []Segment{
		{ID: 3, RateKbps: 800, FileName: "seg_800_3.ts", DurationMS: 4000},
		{ID: 3, RateKbps: 800, FileName: "seg_800_3.copy.ts", DurationMS: 4000},
	}
	if _, err := BuildPlaylist(800, segs); err == nil {
		t.Fatal("BuildPlaylist accepted duplicate ids")
	}
}
