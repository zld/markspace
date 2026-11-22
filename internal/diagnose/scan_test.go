package diagnose

import (
	"testing"

	"mediad/internal/core"
)

func issue(asset, cat string, start, end int64, rec bool) Issue {
	return Issue{
		Category: Category(cat), Stage: StageIngest, Format: "mp4", Asset: asset,
		Span: core.Span{Start: start, End: end}, Recoverable: rec,
	}
}

func TestRangesMergeOverlappingDamage(t *testing.T) {
	got := Ranges(Issues{
		issue("a.mp4", string(CatTruncatedBox), 100, 200, true),
		issue("a.mp4", string(CatIndexOutOfRange), 150, 260, true),
		issue("a.mp4", string(CatMP4Length), 300, 308, false),
	})
	if len(got) != 2 {
		t.Fatalf("ranges=%+v", got)
	}
	// Issues are sorted by start before merging, so the first range covers
	// the truncated tail and the out-of-range run that overlaps its head.
	found := false
	for _, r := range got {
		if r.Span.Start == 100 && r.Span.End == 260 {
			found = true
			// A merged range is recoverable only when every constituent
			// finding was; both inputs here are.
			if len(r.Categories) != 2 || r.Issues != 2 || !r.Recoverable {
				t.Fatalf("merged range=%+v", r)
			}
		}
	}
	if !found {
		t.Fatalf("merged range missing: %+v", got)
	}
}

func TestRangesKeepAssetsSeparate(t *testing.T) {
	got := Ranges(Issues{
		issue("a.mp4", string(CatTruncatedBox), 0, 10, true),
		issue("b.mp4", string(CatTruncatedBox), 0, 10, true),
	})
	if len(got) != 2 {
		t.Fatalf("ranges=%+v", got)
	}
}

func TestRangesIgnoreNonDamageEvents(t *testing.T) {
	wrap := issue("s.rtp", string(CatRTPWrap), 10, 30, true)
	got := Ranges(Issues{wrap})
	if len(got) != 0 {
		t.Fatalf("wrap alone must not form a damaged range: %+v", got)
	}
}

func TestRangesAreDeterministic(t *testing.T) {
	is := Issues{
		issue("b.vtt", string(CatVTTTime), 40, 50, true),
		issue("a.srt", string(CatSRTTime), 10, 20, true),
	}
	a, b := Ranges(is), Ranges(is)
	if len(a) != len(b) {
		t.Fatal("length differs")
	}
	for i := range a {
		if a[i].Asset != b[i].Asset || a[i].Span != b[i].Span {
			t.Fatalf("range %d differs", i)
		}
	}
}

func TestValidateRejectsUnknownAssetAndOverlap(t *testing.T) {
	sizes := map[string]int64{"a.mp4": 100}
	if err := Validate([]Range{{Asset: "b.mp4", Format: "mp4", Span: core.Span{Start: 0, End: 1}, Categories: []string{"x"}, Stages: []string{"ingest"}}}, sizes); err == nil {
		t.Fatal("want unknown asset rejection")
	}
	rs := []Range{
		{Asset: "a.mp4", Format: "mp4", Span: core.Span{Start: 0, End: 50}, Categories: []string{"x"}, Stages: []string{"ingest"}},
		{Asset: "a.mp4", Format: "mp4", Span: core.Span{Start: 10, End: 60}, Categories: []string{"x"}, Stages: []string{"ingest"}},
	}
	if err := Validate(rs, sizes); err == nil {
		t.Fatal("want overlap rejection")
	}
}

func TestIssueValidateBounds(t *testing.T) {
	i := issue("a.mp4", string(CatTruncatedBox), 90, 110, true)
	if err := i.Validate(100); err == nil {
		t.Fatal("want span overrun rejection")
	}
	if err := i.Validate(-1); err != nil {
		t.Fatalf("unbounded validation failed: %v", err)
	}
	if i.Offset() != 90 {
		t.Fatalf("offset=%d", i.Offset())
	}
}

func TestSummaryAndCounts(t *testing.T) {
	is := Issues{
		issue("a.mp4", string(CatTruncatedBox), 0, 10, true),
		issue("a.mp4", string(CatMP4Length), 20, 30, true),
		issue("b.srt", string(CatTruncatedBox), 0, 5, true),
	}
	cc := is.CountByCategory()
	if len(cc) != 2 || cc[0].Category != CatMP4Length || cc[1].Count != 2 {
		t.Fatalf("counts=%+v", cc)
	}
	if is.Summary() != "mp4_length=1,truncated_box=2" {
		t.Fatalf("summary=%s", is.Summary())
	}
}
