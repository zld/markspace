package validate

import (
	"path/filepath"
	"strings"
	"testing"

	"streamcheck/internal/manifest"
)

func seg(id, rate int, ms int64) manifest.Segment {
	return manifest.Segment{ID: id, RateKbps: rate, FileName: manifest.SegmentFileName(manifest.DefaultPattern, rate, id), DurationMS: ms}
}

func TestAnalyzeRateMissingAndProbeFailures(t *testing.T) {
	segs := []manifest.Segment{
		seg(0, 800, 4000), seg(1, 800, 4000),
		{ID: 3, RateKbps: 800, FileName: "seg_800_3.ts", ProbeError: "ffprobe timeout after 5s"},
		{ID: 4, RateKbps: 800, FileName: "seg_800_4.ts", DurationMS: 0},
	}
	lenient := AnalyzeRate(800, segs, nil, false)
	if len(lenient.MissingIDs) != 1 || lenient.MissingIDs[0] != 2 {
		t.Fatalf("missing = %v, want [2]", lenient.MissingIDs)
	}
	if lenient.SegmentCount != 2 || lenient.TotalMS != 8000 {
		t.Fatalf("count/total = %d/%d", lenient.SegmentCount, lenient.TotalMS)
	}
	if len(lenient.ProbeFailures) != 2 {
		t.Fatalf("probe failures = %+v", lenient.ProbeFailures)
	}
	if lenient.ProbeFailures[1].Reason != "zero duration" {
		t.Fatalf("zero-duration reason = %q", lenient.ProbeFailures[1].Reason)
	}

	strict := AnalyzeRate(800, segs, nil, true)
	if len(strict.Issues) == 0 || !strings.Contains(strings.Join(strict.Issues, "\n"), "missing_segment_ids 2") {
		t.Fatalf("strict issues = %v", strict.Issues)
	}
}

func TestAnalyzeRateDuplicates(t *testing.T) {
	dups := map[int][]string{5: {filepath.Join("in", "seg_800_05.ts"), filepath.Join("in", "seg_800_5.ts")}}
	c := AnalyzeRate(800, []manifest.Segment{seg(0, 800, 4000), seg(1, 800, 4000)}, dups, false)
	if len(c.DuplicateIDs) != 1 || c.DuplicateIDs[0] != 5 {
		t.Fatalf("duplicates = %v", c.DuplicateIDs)
	}
	if len(c.Issues) != 1 || !strings.Contains(c.Issues[0], "seg_800_05.ts") || !strings.Contains(c.Issues[0], "seg_800_5.ts") {
		t.Fatalf("issues = %v", c.Issues)
	}
}

func TestFormatRanges(t *testing.T) {
	cases := map[string][]int{
		"":      nil,
		"1":     {1},
		"1-3":   {1, 2, 3},
		"1-2,4": {1, 2, 4},
	}
	for want, ids := range cases {
		if got := FormatRanges(ids); got != want {
			t.Errorf("FormatRanges(%v) = %q, want %q", ids, got, want)
		}
	}
}
