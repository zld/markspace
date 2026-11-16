package validate

import (
	"strings"
	"testing"

	"dashgen/internal/align"
	"dashgen/internal/demo"
	"dashgen/internal/manifest"
	"dashgen/internal/packager"
	"dashgen/internal/timebase"
)

const mediaTemplate = "seg-$RepresentationID$-$Number%05d$.m4s"

func builtDoc(t *testing.T) (*manifest.MPD, *align.Report) {
	t.Helper()
	p := demo.Build()
	report, err := align.Build(p)
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	pkg, err := packager.Build(p, report, mediaTemplate, "")
	if err != nil {
		t.Fatalf("package: %v", err)
	}
	return pkg.MPD, report
}

func TestReconcileHappyPath(t *testing.T) {
	doc, _ := builtDoc(t)
	p := demo.Build()
	tol := Tolerance{StartMs: ms(t, "20ms"), DurationMs: ms(t, "2ms")}
	v, err := Reconcile(doc, p, tol)
	if err != nil {
		t.Fatal(err)
	}
	if v.Failed() {
		t.Fatalf("clean MPD must verify, got failures: %v", v.ReconcileFailures)
	}
	if v.RepresentationCount != 3 {
		t.Errorf("representation_count = %d, want 3", v.RepresentationCount)
	}
	if v.ExpandedSegmentCount != 18 {
		t.Errorf("expanded_segment_count = %d, want 18", v.ExpandedSegmentCount)
	}
	if got := timebase.FormatMillis(v.MaxStartOffset); got != "12.500" {
		t.Errorf("max_start_offset_ms = %s, want 12.500", got)
	}
	if got := timebase.FormatMillis(v.MaxDurationDelta); got != "1.000" {
		t.Errorf("max_duration_delta_ms = %s, want 1.000", got)
	}
	if v.TemplateExample != "seg-r360-00001.m4s" {
		t.Errorf("template_example = %q, want seg-r360-00001.m4s", v.TemplateExample)
	}
}

func TestReconcileToleranceFailure(t *testing.T) {
	doc, _ := builtDoc(t)
	tol := Tolerance{StartMs: ms(t, "10ms"), DurationMs: ms(t, "2ms")}
	v, err := Reconcile(doc, demo.Build(), tol)
	if err != nil {
		t.Fatal(err)
	}
	if !v.Failed() {
		t.Fatal("12.5ms offset must fail a 10ms tolerance during verify")
	}
	if len(v.Tolerance.Violations) == 0 {
		t.Fatal("expected tolerance violations to be attached")
	}
}

func TestReconcileTamperedTicks(t *testing.T) {
	doc, _ := builtDoc(t)
	rep := &doc.Periods[0].AdaptationSets[0].Representations[0]
	rep.SegmentTemplate.SegmentTimeline.Entries[0].Duration += 7
	v, err := Reconcile(doc, demo.Build(),
		Tolerance{StartMs: ms(t, "20ms"), DurationMs: ms(t, "2ms")})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Failed() {
		t.Fatal("tampered MPD ticks must fail reconciliation")
	}
	joined := strings.Join(v.ReconcileFailures, "\n")
	if !strings.Contains(joined, "differ from plan") {
		t.Fatalf("expected tick mismatch message, got: %s", joined)
	}
}

func TestReconcileMissingRepresentation(t *testing.T) {
	doc, _ := builtDoc(t)
	p := demo.Build()
	// Drop a representation from the MPD side.
	set := &doc.Periods[0].AdaptationSets[0]
	set.Representations = set.Representations[:2]
	v, err := Reconcile(doc, p, Tolerance{StartMs: ms(t, "20ms"), DurationMs: ms(t, "2ms")})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(v.ReconcileFailures, "|")
	if !strings.Contains(joined, "missing from MPD") {
		t.Fatalf("expected missing-representation failure, got: %s", joined)
	}
}

func TestReconcileTimescaleMismatch(t *testing.T) {
	doc, _ := builtDoc(t)
	doc.Periods[0].AdaptationSets[0].Representations[0].SegmentTemplate.Timescale = 999
	v, err := Reconcile(doc, demo.Build(),
		Tolerance{StartMs: ms(t, "20ms"), DurationMs: ms(t, "2ms")})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(v.ReconcileFailures, "|")
	if !strings.Contains(joined, "timescale") {
		t.Fatalf("expected timescale mismatch, got: %s", joined)
	}
}

func TestReconcileMissingSegmentInPlan(t *testing.T) {
	doc, _ := builtDoc(t)
	p := demo.Build()
	p.Representations[0].Segments = p.Representations[0].Segments[:5]
	v, err := Reconcile(doc, p,
		Tolerance{StartMs: ms(t, "20ms"), DurationMs: ms(t, "2ms")})
	if err != nil {
		t.Fatal(err)
	}
	if !v.Failed() {
		t.Fatal("plan missing a segment must fail via aligner + count mismatch")
	}
}

func TestReconcileNilInputs(t *testing.T) {
	if _, err := Reconcile(nil, demo.Build(), Tolerance{}); err == nil {
		t.Fatal("nil MPD must error")
	}
	if _, err := Reconcile(&manifest.MPD{}, nil, Tolerance{}); err == nil {
		t.Fatal("nil plan must error")
	}
}
