package fault

import (
	"testing"

	"mediabatch/internal/clock"
)

func sample() List {
	return List{
		Errorf(CodeSegmentHash, SeverityError, ActionIsolated, Source{
			Manifest: "hls/high.m3u8", Segment: "high/seg-2.m4s",
			Loc:   Locator{File: "hls/high.m3u8", Line: 12, Offset: 300},
			Start: clock.MustNew(4, 1), End: clock.MustNew(8, 1),
		}, "hash mismatch"),
		Errorf(CodeMissingDuration, SeverityError, ActionRecorded, Source{
			Manifest: "hls/high.m3u8", Segment: "high/seg-5.m4s",
			Loc:   Locator{File: "hls/high.m3u8", Line: 20, Offset: 500},
			Start: clock.MustNew(12, 1), End: clock.MustNew(16, 1),
		}, "missing EXTINF"),
	}
}

func TestSortAndTraceability(t *testing.T) {
	l := sample()
	l.Sort()
	if l.SortedViolations() != 0 {
		t.Fatal("sort left violations")
	}
	if l.MissingSource() != 0 {
		t.Fatalf("missing source entries: %d", l.MissingSource())
	}
	if l.Errors() != 2 {
		t.Fatalf("errors=%d", l.Errors())
	}
	if l[0].Source.Loc.String() != "hls/high.m3u8:12" {
		t.Fatalf("locator=%s", l[0].Source.Loc)
	}

	broken := List{{Code: CodeParse, Severity: SeverityError, Source: Source{Start: clock.Zero}}}
	if broken.MissingSource() != 1 {
		t.Fatal("expected missing source detection")
	}
	unsorted := List{l[1], l[0]}
	if unsorted.SortedViolations() != 1 {
		t.Fatal("expected ordering violation")
	}
}

func TestDigestStable(t *testing.T) {
	a := sample()
	b := sample()
	if a.Digest() != b.Digest() {
		t.Fatal("identical lists must hash identically")
	}
	b[0].Message = "changed"
	if a.Digest() == b.Digest() {
		t.Fatal("different lists must not hash identically")
	}
	e := NewErr(CodeParse, Locator{File: "m.mpd", Offset: 12}, "bad element")
	if e.Error() != "E_PARSE: bad element (m.mpd@12)" {
		t.Fatalf("err=%s", e)
	}
}
