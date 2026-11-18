package report

import (
	"encoding/json"
	"strings"
	"testing"
)

func sample() Summary {
	return Summary{
		Status: "ok", Representations: 3, Segments: 36, Timescale: 90000,
		DurationMs: 47960, MaxAlignmentErrorMs: 0, ToleranceMs: 20,
		Renditions: []RenditionSummary{{
			ID: "v0", Bandwidth: 800000, SegmentCount: 12,
			FirstSegment: "v0/1.m4s", LastSegment: "v0/12.m4s",
		}},
		Findings: []FindingJSON{{
			Rendition: "v1", Kind: "boundary_misalignment", Detail: "off",
			BoundaryMs: 4033, ErrorMs: 33,
		}},
	}
}

func TestJSONFieldOrderAndShape(t *testing.T) {
	a, err := sample().JSON()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := sample().JSON()
	if string(a) != string(b) {
		t.Fatal("JSON rendering is not deterministic")
	}
	var round Summary
	if err := json.Unmarshal(a, &round); err != nil {
		t.Fatalf("rendered JSON does not parse: %v", err)
	}
	// Declaration order must surface as JSON key order.
	doc := string(a)
	if strings.Index(doc, `"representations"`) > strings.Index(doc, `"segments"`) ||
		strings.Index(doc, `"segments"`) > strings.Index(doc, `"timescale"`) {
		t.Fatalf("JSON field order not stable: %s", doc)
	}
}

func TestTextContainsStableMetricNames(t *testing.T) {
	got := sample().Text()
	for _, want := range []string{
		"representations=3", "segments=36", "timescale=90000",
		"duration_ms=47960", "max_alignment_error_ms=0",
		"first=v0/1.m4s", "last=v0/12.m4s",
		"boundary=4033ms, error=33ms",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("text output missing %q:\n%s", want, got)
		}
	}
}
