package segments

import (
	"strings"
	"testing"

	"dashforge/internal/model"
)

func manifestWith(rm *model.RenditionManifest) *model.Manifest {
	return &model.Manifest{Timescale: 90000, Renditions: []*model.RenditionManifest{rm}}
}

func TestValidateTemplate(t *testing.T) {
	good := []string{
		"v/$RepresentationID$/seg-$Number$.m4s",
		"chunk-$Number%05d$.m4s",
		"t/$Time$/f.mp4",
		"price-$$5/$Bandwidth$.bin",
	}
	for _, g := range good {
		if err := ValidateTemplate(g); err != nil {
			t.Errorf("template %q rejected: %v", g, err)
		}
	}
	bad := []string{"", "   ", "seg-$Number.", "seg-$Oops$.m4s", "seg-$Number%x$.m4s", "dangling-$"}
	for _, b := range bad {
		if err := ValidateTemplate(b); err == nil {
			t.Errorf("template %q should be rejected", b)
		}
	}
}

func TestExpandPlaceholders(t *testing.T) {
	rm := &model.RenditionManifest{
		ID: "720p", Bandwidth: 1_500_000, Timescale: 90000,
		Media:       "v/$RepresentationID$/$Bandwidth$/seg-$Number%04d$-t$Time$.m4s",
		SegmentRoot: "out/720p",
		Segments: []model.SegmentInfo{
			{Number: 1, Start: 0, Duration: 360000},
			{Number: 2, Start: 360000, Duration: 180000},
		},
	}
	res, issues := Expand(manifestWith(rm))
	if len(issues) != 0 {
		t.Fatalf("unexpected issues: %v", issues)
	}
	got := res[0].Segments[0].Path
	want := "v/720p/1500000/seg-0001-t0.m4s"
	if got != want {
		t.Fatalf("expansion = %q, want %q", got, want)
	}
	if res[0].First == "" || res[0].Last == "" || res[0].Count != 2 {
		t.Fatalf("bad summary: %+v", res[0])
	}
}

func TestExpandDetectsGapsAndDuplicates(t *testing.T) {
	rm := &model.RenditionManifest{
		ID: "r", Media: "s/$Number$.m4s", SegmentRoot: "root",
		Segments: []model.SegmentInfo{
			{Number: 1, Start: 0, Duration: 10},
			{Number: 3, Start: 20, Duration: 10}, // gap: 2 missing
			{Number: 4, Start: 20, Duration: 10}, // duplicate timestamp
		},
	}
	_, issues := Expand(manifestWith(rm))
	joined := joinIssues(issues)
	if !strings.Contains(joined, "number_gap") || !strings.Contains(joined, "duplicate_timestamp") {
		t.Fatalf("expected gap and duplicate issues, got %v", issues)
	}
}

func TestExpandRejectsBadAndTraversalTemplates(t *testing.T) {
	rm := &model.RenditionManifest{
		ID: "r", Media: "", SegmentRoot: "root",
		Segments: []model.SegmentInfo{{Number: 1, Start: 0, Duration: 1}},
	}
	if _, issues := Expand(manifestWith(rm)); len(issues) != 1 {
		t.Fatalf("expected one empty-template issue, got %v", issues)
	}

	rm.Media = "../escape/$Number$.m4s"
	_, issues := Expand(manifestWith(rm))
	if len(issues) != 1 || issues[0].Kind != "path_traversal" {
		t.Fatalf("expected traversal issue, got %v", issues)
	}

	rm.Media = "/abs/$Number$.m4s"
	if _, issues := Expand(manifestWith(rm)); issues[0].Kind != "path_traversal" {
		t.Fatalf("expected absolute-path traversal issue, got %v", issues)
	}
}

func TestCheckContained(t *testing.T) {
	if err := checkContained("root", "a/b/../c/seg.m4s"); err != nil {
		t.Errorf("in-root path rejected: %v", err)
	}
	if err := checkContained("root", "a/../../etc/passwd"); err == nil {
		t.Error("escape path should be rejected")
	}
}

func joinIssues(issues []Issue) string {
	parts := make([]string, len(issues))
	for i, is := range issues {
		parts[i] = is.Error()
	}
	return strings.Join(parts, "; ")
}
