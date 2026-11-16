package packager

import (
	"strings"
	"testing"

	"dashgen/internal/align"
	"dashgen/internal/demo"
	"dashgen/internal/timebase"
)

func TestBuildDemo(t *testing.T) {
	report, err := align.Build(demo.Build())
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	pkg, err := Build(demo.Build(), report,
		"seg-$RepresentationID$-$Number%05d$.m4s", "init-$RepresentationID$.mp4")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(pkg.Segments) != 18 {
		t.Fatalf("expanded segments = %d, want 18", len(pkg.Segments))
	}
	if got := pkg.Segments[0].Path; got != "seg-r360-00001.m4s" {
		t.Fatalf("first path = %q, want seg-r360-00001.m4s", got)
	}
	sets := pkg.MPD.Periods[0].AdaptationSets
	if len(sets) != 1 || len(sets[0].Representations) != 3 {
		t.Fatalf("unexpected shape: %+v", sets)
	}
	for _, rep := range sets[0].Representations {
		st := rep.SegmentTemplate
		if st == nil || st.Initialization != "init-$RepresentationID$.mp4" {
			t.Fatalf("rep %s missing init template: %+v", rep.ID, st)
		}
		if st.StartNumber != 1 {
			t.Fatalf("rep %s startNumber = %d, want 1", rep.ID, st.StartNumber)
		}
		if got := st.SegmentTimeline.Entries; len(got) == 0 {
			t.Fatalf("rep %s has empty timeline", rep.ID)
		}
	}
	if pkg.MPD.MediaPresentationDuration == "" || !strings.HasPrefix(pkg.MPD.MediaPresentationDuration, "PT") {
		t.Fatalf("bad presentation duration %q", pkg.MPD.MediaPresentationDuration)
	}
}

func TestBuildRejectsBadTemplate(t *testing.T) {
	report, _ := align.Build(demo.Build())
	if _, err := Build(demo.Build(), report, "seg-$Bogus$.m4s", ""); err == nil {
		t.Fatal("unknown placeholder must fail the build")
	}
	if _, err := Build(demo.Build(), report, "ok-$Number$.m4s", "init-$Bogus$.mp4"); err == nil {
		t.Fatal("bad initialization template must fail the build")
	}
}

func TestBuildRejectsTraversal(t *testing.T) {
	report, _ := align.Build(demo.Build())
	if _, err := Build(demo.Build(), report, "../$Number$.m4s", ""); err == nil {
		t.Fatal("traversal template must fail the build")
	}
}

func TestPresentationDuration(t *testing.T) {
	report, err := align.Build(demo.Build())
	if err != nil {
		t.Fatal(err)
	}
	// Earliest start 0ms (r360), latest end 2024ms (r1080: 48276 @24000).
	d, err := presentationDurationMs(report)
	if err != nil {
		t.Fatal(err)
	}
	lo := timebase.Rational{Num: 2000, Den: 1}
	hi := timebase.Rational{Num: 2025, Den: 1}
	if d.Cmp(lo) < 0 || d.Cmp(hi) > 0 {
		t.Fatalf("duration %v outside expected 2000..2025ms", d)
	}
}
