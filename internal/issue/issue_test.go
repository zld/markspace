package issue

import "testing"

func TestSortIsTotalAndDeterministic(t *testing.T) {
	l := List{
		New(UnsupportedInput, "b", "z"),
		New(MP4Truncated, "a", "y").WithByte(50),
		New(MP4Truncated, "a", "x").WithByte(10),
		New(OverlappingCue, "a", "w").WithLine(9),
		New(OverlappingCue, "a", "w").WithLine(3),
	}
	sorted := l.Sorted()
	want := []string{
		"a:3: overlapping_cue: w",
		"a:9: overlapping_cue: w",
		"a@10: mp4_sample_table_truncated: x",
		"a@50: mp4_sample_table_truncated: y",
		"b: unsupported_input: z",
	}
	for i, w := range want {
		if sorted[i].String() != w {
			t.Fatalf("index %d = %q want %q", i, sorted[i].String(), w)
		}
	}
	if first, ok := l.First(); !ok || first.Loc.Line != 3 {
		t.Fatalf("first=%v", first)
	}
}

func TestRangesMergeAdjacent(t *testing.T) {
	in := []ByteRange{
		{Input: "f", Start: 100, End: 110, Categories: []Category{RTPCorrupt}},
		{Input: "f", Start: 114, End: 120, Categories: []Category{RTPCorrupt}},
		{Input: "f", Start: 500, End: 510, Categories: []Category{MP4Truncated}},
		{Input: "g", Start: 0, End: 5},
	}
	out := MergeRanges(in, 4)
	if len(out) != 3 {
		t.Fatalf("out=%v", out)
	}
	if out[0].Start != 100 || out[0].End != 120 {
		t.Fatalf("merged=%v", out[0])
	}
	if out[2].Input != "g" {
		t.Fatalf("ordering=%v", out)
	}
}

func TestCategoryCountsStable(t *testing.T) {
	l := List{
		New(RTPCorrupt, "a", ""),
		New(RTPCorrupt, "a", ""),
		New(InvalidTime, "b", ""),
	}
	c := l.Counts()
	if c[RTPCorrupt] != 2 || c[InvalidTime] != 1 {
		t.Fatalf("counts=%v", c)
	}
	cats := l.Categories()
	if len(cats) != 2 || cats[0] != string(InvalidTime) {
		t.Fatalf("categories=%v", cats)
	}
}
