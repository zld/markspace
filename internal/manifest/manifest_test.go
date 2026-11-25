package manifest

import (
	"strings"
	"testing"

	"replaycheck/internal/policy"
	"replaycheck/internal/split"
	"replaycheck/internal/timeline"
)

func digestOf(seed byte) string { return strings.Repeat(string([]byte{'a' + seed}), 64) }

func fixture() (*timeline.Timeline, *split.Plan, *policy.Result, []ClipRef) {
	tl := &timeline.Timeline{Base: 1_000_000, SourceDigest: digestOf(0)}
	plan := &split.Plan{Segments: []split.Segment{
		{Index: 0, Span: timeline.Span{Start: 0, End: 1000}, Samples: 3},
		{Index: 1, Span: timeline.Span{Start: 1000, End: 2000}, Samples: 4},
	}}
	plan.Segments[0].StartName, plan.Segments[0].EndName = "open", "cut"
	plan.Segments[1].StartName, plan.Segments[1].EndName = "cut", "close"
	res := &policy.Result{MinQualityPPM: 920_000, PolicyDigest: digestOf(1), Choices: []policy.Choice{
		{Segment: 0, Level: "compact", QualityPPM: 920_000, Bytes: 54_000},
		{Segment: 1, Level: "compact", QualityPPM: 920_000, Bytes: 54_000},
	}}
	clips := []ClipRef{
		{Segment: 0, Digest: digestOf(2), Bytes: 54_000, Level: "compact", QualityPPM: 920_000},
		{Segment: 1, Digest: digestOf(3), Bytes: 54_000, Level: "compact", QualityPPM: 920_000},
	}
	return tl, plan, res, clips
}

func checker(known map[string]int64) BlobCheck {
	return func(d string) (string, int64, error) {
		n, ok := known[d]
		if !ok {
			return "", 0, nil
		}
		return d, n, nil
	}
}

func TestBuildAndVerify(t *testing.T) {
	tl, plan, res, clips := fixture()
	m, err := Build("normal", tl, plan, res, clips, "evdigest")
	if err != nil {
		t.Fatal(err)
	}
	known := map[string]int64{digestOf(2): 54_000, digestOf(3): 54_000}
	rep, err := Verify(m, tl.SourceDigest, res.PolicyDigest, 900_000, checker(known))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if !rep.Valid || rep.TotalBytes != rep.SummedBytes || rep.TotalBytes != 108_000 {
		t.Fatalf("report %+v", rep)
	}
}

func TestVerifyDetectsGap(t *testing.T) {
	tl, plan, res, clips := fixture()
	m, _ := Build("normal", tl, plan, res, clips, "ev")
	m.Segments[1].StartTick = 1200
	d, _ := Digest(m)
	m.Digest = d
	rep, err := Verify(m, tl.SourceDigest, res.PolicyDigest, 900_000, nil)
	if err == nil || rep.Valid {
		t.Fatal("gap not detected")
	}
}

func TestVerifyDetectsByteTamper(t *testing.T) {
	tl, plan, res, clips := fixture()
	m, _ := Build("normal", tl, plan, res, clips, "ev")
	known := map[string]int64{digestOf(2): 54_000, digestOf(3): 54_000}
	m.TotalBytes = 1
	d, _ := Digest(m)
	m.Digest = d
	rep, err := Verify(m, tl.SourceDigest, res.PolicyDigest, 900_000, checker(known))
	if err == nil || rep.Valid {
		t.Fatal("tampered total not detected")
	}
}

func TestVerifyDetectsMissingArtifact(t *testing.T) {
	tl, plan, res, clips := fixture()
	m, _ := Build("normal", tl, plan, res, clips, "ev")
	// Artifact digest is well-formed but storage has different bytes.
	tampered := func(string) (string, int64, error) { return digestOf(9), 54_000, nil }
	rep, err := Verify(m, tl.SourceDigest, res.PolicyDigest, 900_000, tampered)
	if err == nil || rep.Valid {
		t.Fatal("artifact mismatch not detected")
	}
}

func TestVerifyDetectsQualityFloor(t *testing.T) {
	tl, plan, res, clips := fixture()
	m, _ := Build("normal", tl, plan, res, clips, "ev")
	rep, err := Verify(m, tl.SourceDigest, res.PolicyDigest, 950_000, nil)
	if err == nil || rep.Valid {
		t.Fatalf("floor violation not detected: %+v", rep)
	}
}

func TestCompareReportsEachField(t *testing.T) {
	tl, plan, res, clips := fixture()
	a, _ := Build("normal", tl, plan, res, clips, "ev")
	b, _ := Build("normal", tl, plan, res, clips, "ev")
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
