package verify

import (
	"strings"
	"testing"

	"archivecheck/internal/manifest"
	"archivecheck/internal/media"
	"archivecheck/internal/recovery"
)

func baseResults() []recovery.Result {
	return []recovery.Result{
		{
			Layer: "low", StreamLen: 3000, DurationMs: 1000, ParseRatio: 1,
			Anchors:  []recovery.Anchor{{TimeMs: 0, Offset: 0}, {TimeMs: 250, Offset: 700}, {TimeMs: 500, Offset: 1500}},
			Segments: []recovery.Segment{{StartMs: 0, EndMs: 250, ByteStart: 0, ByteEnd: 700}, {StartMs: 250, EndMs: 500, ByteStart: 700, ByteEnd: 1500}, {StartMs: 500, EndMs: 1000, ByteStart: 1500, ByteEnd: 3000}},
			MeanKFMs: 250, MaxKFMs: 250,
		},
	}
}

func baseMerge() recovery.Merge {
	return recovery.Merge{ToleranceMs: 2, Layers: []string{"low"}}
}

func baseDoc() manifest.Document { return manifest.Document{SHA256: "abc"} }

func TestVerifyHealthy(t *testing.T) {
	r := Verify(baseResults(), baseMerge(), baseDoc())
	if !r.OK() {
		t.Fatalf("violations=%+v", r.Violations)
	}
	if len(r.Checked) != 4 {
		t.Fatalf("checked=%+v", r.Checked)
	}
	if r.SHA256 == "" {
		t.Fatal("missing digest")
	}
}

func TestVerifyDigestDeterministic(t *testing.T) {
	a := Verify(baseResults(), baseMerge(), baseDoc())
	b := Verify(baseResults(), baseMerge(), baseDoc())
	if a.SHA256 != b.SHA256 {
		t.Fatalf("digest differs: %s vs %s", a.SHA256, b.SHA256)
	}
	// Any changed evidence must change the digest.
	results := baseResults()
	results[0].MeanKFMs = 251
	c := Verify(results, baseMerge(), baseDoc())
	doc2 := baseDoc()
	doc2.SHA256 = "def"
	d := Verify(baseResults(), baseMerge(), doc2)
	if c.SHA256 == a.SHA256 || d.SHA256 == a.SHA256 {
		t.Fatal("digest did not change with evidence")
	}
}

func TestVerifyDetectsNonMonotonicTime(t *testing.T) {
	results := baseResults()
	results[0].Anchors[2].TimeMs = 100
	r := Verify(results, baseMerge(), baseDoc())
	if !hasInvariant(r, TimeMonotonic) {
		t.Fatalf("violations=%+v", r.Violations)
	}
}

func TestVerifyDetectsOutOfBoundsOffsets(t *testing.T) {
	results := baseResults()
	results[0].Segments[1].ByteEnd = 5000
	r := Verify(results, baseMerge(), baseDoc())
	if !hasInvariant(r, OffsetBounded) {
		t.Fatalf("violations=%+v", r.Violations)
	}
}

func TestVerifyDetectsUnexplainedGap(t *testing.T) {
	results := baseResults()
	results[0].Gaps = []recovery.Gap{{StartMs: 500, EndMs: 750, ByteStart: 1500, ByteEnd: 2100}}
	r := Verify(results, baseMerge(), baseDoc())
	if !hasInvariant(r, GapsExplained) {
		t.Fatalf("violations=%+v", r.Violations)
	}

	// A well-explained gap passes.
	results = baseResults()
	results[0].Gaps = []recovery.Gap{{StartMs: 500, EndMs: 750, ByteStart: 1500, ByteEnd: 2100,
		Reason: media.KindOBUTruncated, Detail: "cut"}}
	if r := Verify(results, baseMerge(), baseDoc()); !r.OK() {
		t.Fatalf("violations=%+v", r.Violations)
	}

	// A zero-width gap is not an explanation.
	results = baseResults()
	results[0].Gaps = []recovery.Gap{{StartMs: 500, EndMs: 500, Reason: media.KindGap, Detail: "x"}}
	if r := Verify(results, baseMerge(), baseDoc()); !hasInvariant(r, GapsExplained) {
		t.Fatalf("violations=%+v", r.Violations)
	}
}

func TestVerifyDetectsConflictBelowTolerance(t *testing.T) {
	m := baseMerge()
	m.Conflicts = []recovery.Conflict{{RefMs: 250, Tolerance: 5, Spread: 2}}
	r := Verify(baseResults(), m, baseDoc())
	if !hasInvariant(r, BoundaryAligned) {
		t.Fatalf("violations=%+v", r.Violations)
	}

	m = baseMerge()
	m.ToleranceMs = 0
	if r := Verify(baseResults(), m, baseDoc()); !hasInvariant(r, BoundaryAligned) {
		t.Fatalf("violations=%+v", r.Violations)
	}
}

func TestReportTextStable(t *testing.T) {
	r := Verify(baseResults(), baseMerge(), baseDoc())
	text := r.Text()
	if !strings.Contains(text, "verify checked=time_monotonic,offset_bounded,gaps_explained,boundary_aligned violations=0") {
		t.Fatalf("text=%q", text)
	}
	if !strings.Contains(text, "verify_sha256="+r.SHA256) {
		t.Fatalf("text=%q", text)
	}
}

func hasInvariant(r Report, name string) bool {
	for _, v := range r.Violations {
		if v.Invariant == name {
			return true
		}
	}
	return false
}

func TestVerifySources(t *testing.T) {
	src := Source{Layer: "low", StreamLen: 100, Units: []recovery.Unit{
		{Offset: 0, End: 30}, {Offset: 30, End: 70}, {Offset: 70, End: 100},
	}}
	r := VerifySources(baseResults(), baseMerge(), baseDoc(), []Source{src})
	if !r.OK() {
		t.Fatalf("violations=%+v", r.Violations)
	}
	if len(r.Checked) != 6 {
		t.Fatalf("checked=%+v", r.Checked)
	}

	bad := Source{Layer: "low", StreamLen: 100, Units: []recovery.Unit{
		{Offset: 0, End: 50}, {Offset: 40, End: 60}, {Offset: 60, End: 140},
	}}
	r = VerifySources(baseResults(), baseMerge(), baseDoc(), []Source{bad})
	if !hasInvariant(r, SourceMonotonic) || !hasInvariant(r, SourceBounded) {
		t.Fatalf("violations=%+v", r.Violations)
	}

	truncated := Source{Layer: "high", StreamLen: 100, Units: []recovery.Unit{
		{Offset: 0, End: 60}, {Offset: 60, End: 180, Truncated: true},
	}}
	if r := VerifySources(baseResults(), baseMerge(), baseDoc(), []Source{truncated}); !r.OK() {
		t.Fatalf("truncated unit rejected a declared overshoot: %+v", r.Violations)
	}
}
