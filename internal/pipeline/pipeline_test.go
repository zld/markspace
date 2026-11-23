package pipeline

import (
	"context"
	"strings"
	"testing"
)

func cfg(c string) Config {
	return Config{Case: c, Seed: 23, ChunkBytes: 7, Queue: 2,
		Targets: []float64{900, 1800, 3600}, ConsumerDelayMs: 0, ToleranceMs: 2, WindowMs: 250}
}

func TestRunHealthyAcceptance(t *testing.T) {
	out, err := Run(context.Background(), cfg("ok"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Damaged {
		t.Fatalf("healthy run flagged damaged: %+v", out.Manifest.Faults)
	}
	if !out.Verify.OK() {
		t.Fatalf("violations=%+v", out.Verify.Violations)
	}
	if out.PeakBuffered > out.QueueBound {
		t.Fatalf("peak=%d bound=%d", out.PeakBuffered, out.QueueBound)
	}
	if out.QueueBound != 6 {
		t.Fatalf("queue bound=%d", out.QueueBound)
	}
	if out.Manifest.ManifestDriftMs > 2 {
		t.Fatalf("drift=%d", out.Manifest.ManifestDriftMs)
	}
	if out.Manifest.Quality.MinParseRatio < 0.95 {
		t.Fatalf("parse ratio=%f", out.Manifest.Quality.MinParseRatio)
	}
	if out.Manifest.Quality.MaxBitrateErrorPct > 5 {
		t.Fatalf("bitrate error=%f", out.Manifest.Quality.MaxBitrateErrorPct)
	}
	if len(out.Manifest.Layers) != 3 {
		t.Fatalf("layers=%d", len(out.Manifest.Layers))
	}
	for _, l := range out.Manifest.Layers {
		if l.MeanKeyframeIntervalMs != 250 || l.MaxKeyframeIntervalMs != 250 {
			t.Fatalf("layer %s intervals mean=%f max=%d", l.Layer, l.MeanKeyframeIntervalMs, l.MaxKeyframeIntervalMs)
		}
		if l.RecoverableGaps != 0 || l.UnrecoverableGaps != 0 {
			t.Fatalf("layer %s gaps=%+v", l.Layer, l)
		}
	}
}

func TestRunDigestStableAcrossRepeats(t *testing.T) {
	a, err := Run(context.Background(), cfg("ok"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(context.Background(), cfg("ok"))
	if err != nil {
		t.Fatal(err)
	}
	if a.Manifest.SHA256 != b.Manifest.SHA256 {
		t.Fatalf("manifest digest differs: %s vs %s", a.Manifest.SHA256, b.Manifest.SHA256)
	}
	if a.Verify.SHA256 != b.Verify.SHA256 {
		t.Fatalf("verify digest differs: %s vs %s", a.Verify.SHA256, b.Verify.SHA256)
	}
}

// TestRunChunkSizeInvariance checks that recovered evidence is independent of
// how the bytes were split. Chunk-bytes is part of the run identity (so the
// manifest digest legitimately differs), but every boundary, segment, gap and
// quality statistic must be identical.
func TestRunChunkSizeInvariance(t *testing.T) {
	baseline, err := Run(context.Background(), cfg("ok"))
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range []int{3, 11, 64, 4096} {
		c := cfg("ok")
		c.ChunkBytes = chunk
		out, err := Run(context.Background(), c)
		if err != nil {
			t.Fatalf("chunk=%d: %v", chunk, err)
		}
		if !out.Verify.OK() || len(out.Verify.Violations) != len(baseline.Verify.Violations) {
			t.Fatalf("chunk=%d verification changed: %+v", chunk, out.Verify.Violations)
		}
		if out.Manifest.ManifestDriftMs != baseline.Manifest.ManifestDriftMs {
			t.Fatalf("chunk=%d drift=%d", chunk, out.Manifest.ManifestDriftMs)
		}
		if len(out.Manifest.Layers) != len(baseline.Manifest.Layers) {
			t.Fatalf("chunk=%d layer count=%d", chunk, len(out.Manifest.Layers))
		}
		for i := range out.Manifest.Layers {
			a, b := out.Manifest.Layers[i], baseline.Manifest.Layers[i]
			if a.Layer != b.Layer || a.RecoveredSegments != b.RecoveredSegments ||
				a.ParseRatio != b.ParseRatio || a.ActualKbps != b.ActualKbps {
				t.Fatalf("chunk=%d layer row changed: %+v vs %+v", chunk, a, b)
			}
			if len(a.Segments) != len(b.Segments) {
				t.Fatalf("chunk=%d segments changed", chunk)
			}
			for j := range a.Segments {
				if a.Segments[j] != b.Segments[j] {
					t.Fatalf("chunk=%d segment %d changed: %+v vs %+v", chunk, j, a.Segments[j], b.Segments[j])
				}
			}
		}
	}
}

func TestRunSlowConsumerStillBounded(t *testing.T) {
	c := cfg("ok")
	c.ConsumerDelayMs = 15
	out, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if out.PeakBuffered > out.QueueBound {
		t.Fatalf("peak=%d bound=%d", out.PeakBuffered, out.QueueBound)
	}
	if !out.Verify.OK() {
		t.Fatalf("violations=%+v", out.Verify.Violations)
	}
}

func TestRunCorruptIndex(t *testing.T) {
	out, err := Run(context.Background(), cfg("corrupt-index"))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Damaged {
		t.Fatal("damage not flagged")
	}
	if !out.Verify.OK() {
		t.Fatalf("violations=%+v", out.Verify.Violations)
	}
	if !hasFault(out.Manifest.Faults, "index_out_of_range") {
		t.Fatalf("faults=%+v", out.Manifest.Faults)
	}
	var gaps int
	for _, l := range out.Manifest.Layers {
		gaps += l.RecoverableGaps + l.UnrecoverableGaps
		if l.Layer == "mid" {
			if l.RecoveredSegments >= 4 {
				t.Fatalf("mid was not resynchronized: %+v", l)
			}
			for _, g := range l.Gaps {
				if g.Reason != "index_out_of_range" {
					t.Fatalf("gap reason=%q", g.Reason)
				}
			}
		}
	}
	if gaps == 0 {
		t.Fatal("no gap retained for the corrupt index")
	}
}

func TestRunBoundarySkew(t *testing.T) {
	out, err := Run(context.Background(), cfg("boundary-skew"))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Damaged {
		t.Fatal("damage not flagged")
	}
	if !hasFault(out.Manifest.Faults, "boundary_conflict") {
		t.Fatalf("faults=%+v", out.Manifest.Faults)
	}
	if out.Manifest.BoundaryConflicts == 0 || out.Manifest.ManifestDriftMs != 8 {
		t.Fatalf("conflicts=%d drift=%d", out.Manifest.BoundaryConflicts, out.Manifest.ManifestDriftMs)
	}
	if out.Manifest.BoundaryDiffsMs["mid"] != 8 {
		t.Fatalf("diffs=%+v", out.Manifest.BoundaryDiffsMs)
	}
	// Evidence from every layer is preserved, including the outlier.
	if out.Manifest.BoundaryDiffsMs["low"] != 0 || out.Manifest.BoundaryDiffsMs["high"] != 0 {
		t.Fatalf("diffs=%+v", out.Manifest.BoundaryDiffsMs)
	}
}

func TestRunOBUTruncation(t *testing.T) {
	out, err := Run(context.Background(), cfg("obu-truncated"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasFault(out.Manifest.Faults, "obu_truncated") {
		t.Fatalf("faults=%+v", out.Manifest.Faults)
	}
	for _, l := range out.Manifest.Layers {
		if l.Layer != "high" {
			continue
		}
		if l.UnrecoverableGaps != 1 {
			t.Fatalf("high gaps=%+v", l)
		}
		if l.ParseRatio >= 1 {
			t.Fatalf("parse ratio=%f", l.ParseRatio)
		}
		for _, g := range l.Gaps {
			if g.Reason != "obu_truncated" || g.Class != "unrecoverable" {
				t.Fatalf("gap=%+v", g)
			}
		}
	}
	if !out.Verify.OK() {
		t.Fatalf("violations=%+v", out.Verify.Violations)
	}
}

func TestRunKeyframeRegress(t *testing.T) {
	out, err := Run(context.Background(), cfg("keyframe-regress"))
	if err != nil {
		t.Fatal(err)
	}
	if !hasFault(out.Manifest.Faults, "index_time_regress") {
		t.Fatalf("faults=%+v", out.Manifest.Faults)
	}
}

func TestRunSplitStartCodeIsHealthy(t *testing.T) {
	out, err := Run(context.Background(), cfg("split-startcode"))
	if err != nil {
		t.Fatal(err)
	}
	if out.Damaged {
		t.Fatalf("reassembled start codes flagged as damage: %+v", out.Manifest.Faults)
	}
	if !hasFault(out.Manifest.Faults, "start_code_split") {
		t.Fatalf("split start codes not reported: %+v", out.Manifest.Faults)
	}
}

func TestRunCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, cfg("ok"))
	if err == nil || !strings.Contains(err.Error(), "fan-out") && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("err=%v", err)
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	cases := []Config{
		{Case: "nope", ChunkBytes: 1, Queue: 1, ToleranceMs: 1, WindowMs: 1},
		{Case: "ok", ChunkBytes: 0, Queue: 1, ToleranceMs: 1, WindowMs: 1},
		{Case: "ok", ChunkBytes: 1, Queue: 0, ToleranceMs: 1, WindowMs: 1},
		{Case: "ok", ChunkBytes: 1, Queue: 1, ToleranceMs: 0, WindowMs: 1},
		{Case: "ok", ChunkBytes: 1, Queue: 1, ToleranceMs: 1, WindowMs: 0},
		{Case: "ok", ChunkBytes: 1, Queue: 1, ToleranceMs: 1, WindowMs: 1, ConsumerDelayMs: -1},
	}
	for i, c := range cases {
		if _, err := Run(context.Background(), c); err == nil {
			t.Fatalf("case %d accepted", i)
		}
	}
}

func TestRunBackpressureWithTinyQueue(t *testing.T) {
	c := cfg("ok")
	c.Queue = 1
	c.ChunkBytes = 64
	out, err := Run(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if out.QueueBound != 3 {
		t.Fatalf("bound=%d", out.QueueBound)
	}
	if out.PeakBuffered > out.QueueBound {
		t.Fatalf("peak=%d bound=%d", out.PeakBuffered, out.QueueBound)
	}
	if !out.Verify.OK() {
		t.Fatalf("violations=%+v", out.Verify.Violations)
	}
}

func hasFault(faults []string, kind string) bool {
	for _, f := range faults {
		if strings.HasPrefix(f, kind+" ") {
			return true
		}
	}
	return false
}
