package manifest

import (
	"strings"
	"testing"

	"replaycheck/internal/policy"
	"replaycheck/internal/split"
	"replaycheck/internal/timeline"
)

func fixture() (*timeline.Timeline, *split.Plan, *policy.Result) {
	tl := &timeline.Timeline{Base: 1_000_000, SourceDigest: strings.Repeat("a0", 32)}
	plan := &split.Plan{Segments: []split.Segment{
		{Index: 0, Span: timeline.Span{Start: 0, End: 1000}, Samples: 3},
		{Index: 1, Span: timeline.Span{Start: 1000, End: 2000}, Samples: 4},
	}}
	plan.Segments[0].StartName, plan.Segments[0].EndName = "open", "cut"
	plan.Segments[1].StartName, plan.Segments[1].EndName = "cut", "close"
	res := &policy.Result{MinQualityPPM: 920_000, PolicyDigest: strings.Repeat("b0", 32), Choices: []policy.Choice{
		{Segment: 0, Level: "compact", QualityPPM: 920_000, Bytes: 54_000},
		{Segment: 1, Level: "compact", QualityPPM: 920_000, Bytes: 54_000},
	}}
	return tl, plan, res
}

func TestBuildAndVerify(t *testing.T) {
	tl, plan, res := fixture()
	m, err := Build("normal", tl, plan, res, "evdigest")
	if err != nil {
		t.Fatal(err)
	}
	rep, err := Verify(m, tl.SourceDigest, res.PolicyDigest, 900_000)
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.Valid || rep.TotalBytes != rep.SummedBytes || rep.TotalBytes != 108_000 {
		t.Fatalf("report %+v", rep)
	}
}

func TestVerifyDetectsGap(t *testing.T) {
	tl, plan, res := fixture()
	m, _ := Build("normal", tl, plan, res, "ev")
	m.Segments[1].StartTick = 1200 // create a gap
	d, _ := Digest(m)
	m.Digest = d
	rep, err := Verify(m, tl.SourceDigest, res.PolicyDigest, 900_000)
	if err == nil || rep.Valid {
		t.Fatal("gap not detected")
	}
}

func TestVerifyDetectsByteTamper(t *testing.T) {
	tl, plan, res := fixture()
	m, _ := Build("normal", tl, plan, res, "ev")
	m.Segments[0].Bytes = 1
	d, _ := Digest(m)
	m.Digest = d // even with a fresh manifest digest, totals and derived digests disagree
	rep, err := Verify(m, tl.SourceDigest, res.PolicyDigest, 900_000)
	if err == nil || rep.Valid {
		t.Fatal("tampered bytes not detected")
	}
}

func TestCompareReportsEachField(t *testing.T) {
	tl, plan, res := fixture()
	a, _ := Build("normal", tl, plan, res, "ev")
	b, _ := Build("normal", tl, plan, res, "ev")
	if d := Compare(a, b); !d.Same || d.Count != 0 {
		t.Fatalf("identical manifests differ: %+v", d)
	}
	b.Segments[1].QualityPPM = 900_000
	b.TotalBytes--
	d, _ := Digest(b)
	b.Digest = d
	diff := Compare(a, b)
	if diff.Same || diff.Count < 2 {
		t.Fatalf("expected per-field differences, got %+v", diff)
	}
}
