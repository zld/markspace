package manifest

import "testing"

func TestCompilePatternAndMatch(t *testing.T) {
	p, err := CompilePattern(DefaultPattern)
	if err != nil {
		t.Fatalf("CompilePattern: %v", err)
	}
	rate, id, ok := p.Match("seg_1600_0012.ts")
	if !ok || rate != 1600 || id != 12 {
		t.Fatalf("Match = %d, %d, %v; want 1600, 12, true", rate, id, ok)
	}
	if _, _, ok := p.Match("seg_1600.ts"); ok {
		t.Fatal("name without index must not match")
	}
	if _, _, ok := p.Match("seg_1600_x.ts"); ok {
		t.Fatal("non-numeric index must not match")
	}

	q, err := CompilePattern("chunk-{index}.m4s")
	if err != nil {
		t.Fatalf("CompilePattern: %v", err)
	}
	rate, id, ok = q.Match("chunk-7.m4s")
	if !ok || rate != -1 || id != 7 {
		t.Fatalf("Match = %d, %d, %v; want -1, 7, true", rate, id, ok)
	}
}

func TestCompilePatternRejectsInvalidRules(t *testing.T) {
	for _, tc := range []string{"", " {index}.ts", "sub/{index}.ts", "seg.ts",
		"seg_{index}_{index}.ts", "seg_{id}.ts", "seg_{index.ts"} {
		if _, err := CompilePattern(tc); err == nil {
			t.Errorf("CompilePattern(%q) succeeded, want error", tc)
		}
	}
}

func TestSegmentFileName(t *testing.T) {
	if got := SegmentFileName(DefaultPattern, 800, 3); got != "seg_800_3.ts" {
		t.Fatalf("SegmentFileName = %q", got)
	}
}

func TestFormattingAndTargetDuration(t *testing.T) {
	if got := FormatEXTINF(4020); got != "4.020" {
		t.Fatalf("FormatEXTINF(4020) = %q", got)
	}
	if got := FormatEXTINF(24000); got != "24.000" {
		t.Fatalf("FormatEXTINF(24000) = %q", got)
	}
	segs := []PlaylistSegment{{DurationMS: 4000}, {DurationMS: 4001}}
	if got := TargetDurationSeconds(segs); got != 5 {
		t.Fatalf("TargetDurationSeconds = %d, want 5", got)
	}
	var total int64
	for _, s := range segs {
		total += s.DurationMS
	}
	p := Playlist{Segments: segs}
	if got := p.TotalDurationMS(); got != total {
		t.Fatalf("TotalDurationMS = %d, want %d", got, total)
	}
}
