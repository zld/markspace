package manifest

import (
	"encoding/json"
	"strings"
	"testing"

	"archivecheck/internal/media"
	"archivecheck/internal/recovery"
)

func inputs() ([]LayerInput, recovery.Merge) {
	low := recovery.Result{
		Layer: "low", Clock: media.MustClock(1000), StreamLen: 3000, DurationMs: 1000,
		Segments: []recovery.Segment{{StartMs: 0, EndMs: 250, ByteStart: 0, ByteEnd: 700},
			{StartMs: 250, EndMs: 500, ByteStart: 700, ByteEnd: 1500},
			{StartMs: 500, EndMs: 1000, ByteStart: 1500, ByteEnd: 3000}},
		ParseRatio: 1, MeanKFMs: 250, MaxKFMs: 250, StartMs: 0, EndMs: 1000,
	}
	mid := recovery.Result{
		Layer: "mid", Clock: media.MustClock(1000), StreamLen: 2000, DurationMs: 1000,
		Segments: []recovery.Segment{{StartMs: 0, EndMs: 500, ByteStart: 0, ByteEnd: 1000},
			{StartMs: 500, EndMs: 1000, ByteStart: 1000, ByteEnd: 2000}},
		Gaps: []recovery.Gap{{StartMs: 250, EndMs: 500, ByteStart: 500, ByteEnd: 1000,
			Reason: media.KindOBUTruncated, Detail: "cut", Class: media.Unrecoverable}},
		ParseRatio: 0.9, MeanKFMs: 250, MaxKFMs: 500, StartMs: 0, EndMs: 1000,
	}
	m := recovery.Merge{ToleranceMs: 2, DriftMs: 8, Layers: []string{"low", "mid"},
		Conflicts:  []recovery.Conflict{{RefMs: 250, Tolerance: 2, Spread: 8}},
		Boundaries: []recovery.SharedBoundary{{RefMs: 250, Times: map[string]int64{"low": 250, "mid": 258}}}}
	return []LayerInput{
		{Result: low, Codec: "h264", Units: 20, Keyframes: 4, TargetKbps: 900, ActualKbps: 900, ErrorPct: 0},
		{Result: mid, Codec: "h265", Units: 21, Keyframes: 4, TargetKbps: 1800, ActualKbps: 1750, ErrorPct: -2.7},
	}, m
}

func build(t *testing.T) Document {
	t.Helper()
	in, m := inputs()
	doc, err := Build(Document{Version: 1, Case: "ok", Seed: 23, ChunkBytes: 7, QueueDepth: 2,
		DurationMs: 1000, ToleranceMs: 2, Faults: []string{"b_fault", "a_fault"}}, in, m)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestBuildSortsAndAggregates(t *testing.T) {
	doc := build(t)
	if len(doc.Layers) != 2 || doc.Layers[0].Layer != "low" {
		t.Fatalf("layers=%+v", doc.Layers)
	}
	if doc.Layers[1].UnrecoverableGaps != 1 || doc.Layers[1].RecoverableGaps != 0 {
		t.Fatalf("gap classes=%+v", doc.Layers[1])
	}
	if doc.BoundaryConflicts != 1 || doc.ManifestDriftMs != 8 {
		t.Fatalf("conflicts=%d drift=%d", doc.BoundaryConflicts, doc.ManifestDriftMs)
	}
	if doc.Faults[0] != "a_fault" {
		t.Fatalf("faults not sorted: %+v", doc.Faults)
	}
	q := doc.Quality
	if q.Layers != 2 || q.Segments != 5 || q.Gaps != 1 {
		t.Fatalf("quality=%+v", q)
	}
	if q.MinParseRatio != 0.9 || q.MaxKeyframeIntervalMs != 500 {
		t.Fatalf("quality=%+v", q)
	}
	if q.Complete {
		t.Fatal("incomplete asset reported complete")
	}
	if absf(q.MaxBitrateErrorPct-2.7) > 1e-9 {
		t.Fatalf("max error=%f", q.MaxBitrateErrorPct)
	}
}

func TestDigestIsDeterministicAndContentSensitive(t *testing.T) {
	a := build(t)
	b := build(t)
	if a.SHA256 == "" || a.SHA256 != b.SHA256 {
		t.Fatalf("digests differ: %q vs %q", a.SHA256, b.SHA256)
	}
	in, m := inputs()
	in[0].ActualKbps = 901
	c, err := Build(Document{Version: 1, Case: "ok", Seed: 23, ChunkBytes: 7, QueueDepth: 2,
		DurationMs: 1000, ToleranceMs: 2, Faults: []string{"b_fault", "a_fault"}}, in, m)
	if err != nil {
		t.Fatal(err)
	}
	if c.SHA256 == a.SHA256 {
		t.Fatal("digest ignored a content change")
	}
}

func TestJSONRoundTrip(t *testing.T) {
	doc := build(t)
	b, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var back Document
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.SHA256 != doc.SHA256 || len(back.Layers) != 2 {
		t.Fatalf("round trip=%+v", back)
	}
}

func TestTextContainsRequiredFields(t *testing.T) {
	text := build(t).Text()
	required := []string{
		"codec=", "units=", "keyframes=", "recoverable_gaps=",
		"target_kbps=", "actual_kbps=", "bitrate_error_pct=", "parse_ratio=",
		"mean_keyframe_interval_ms=", "max_keyframe_interval_ms=",
		"manifest_drift_ms=", "sha256=", "boundary_diff layer=mid",
	}
	for _, r := range required {
		if !strings.Contains(text, r) {
			t.Fatalf("missing %q in:\n%s", r, text)
		}
	}
}

func TestBuildEmpty(t *testing.T) {
	doc, err := Build(Document{Version: 1}, nil, recovery.Merge{ToleranceMs: 1})
	if err != nil {
		t.Fatal(err)
	}
	if doc.Quality.Layers != 0 || doc.Quality.MinParseRatio != 1 {
		t.Fatalf("quality=%+v", doc.Quality)
	}
}
