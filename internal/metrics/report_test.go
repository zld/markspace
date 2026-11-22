package metrics

import (
	"strings"
	"testing"

	"mediacore/internal/clock"
)

func TestQualityDistribution(t *testing.T) {
	sizes := make([]uint32, 100)
	for i := range sizes {
		sizes[i] = uint32(100 + i) // 100..199
	}
	q, err := ComputeQuality(sizes)
	if err != nil {
		t.Fatal(err)
	}
	if q.MinBytes != 100 || q.MaxBytes != 199 {
		t.Fatalf("min/max wrong: %+v", q)
	}
	if q.MeanBytes != 150 || q.P50 < 149 || q.P50 > 151 {
		t.Fatalf("mean/p50 wrong: %+v", q)
	}
	// nearest-rank: p99 -> rank 99 (1-based) -> index 98 -> value 198.
	if q.P99 != 198 {
		t.Fatalf("p99 = %d, want 198", q.P99)
	}
	sorted := append([]uint32(nil), sizes...)
	if percentile(sorted, 100) != 199 {
		t.Fatal("p100 must equal max")
	}
	// input slice must not be mutated
	if sizes[0] != 100 || sizes[99] != 199 {
		t.Fatal("input mutated")
	}
}

func TestBitrateAndDeviation(t *testing.T) {
	// 1.8 Mbps target: 225000 bytes over 1 s.
	bps, err := Bitrate(225000, clock.Nanos(clock.TicksPerSecond))
	if err != nil {
		t.Fatal(err)
	}
	if bps != 1_800_000 {
		t.Fatalf("bps = %d", bps)
	}
	dev, err := DeviationHundredths(1_800_000, 1_818_000) // +1.0%
	if err != nil {
		t.Fatal(err)
	}
	if dev != 100 {
		t.Fatalf("dev = %d", dev)
	}
	if _, err := Bitrate(1, 0); clock.Classify(err) != clock.KindInvalidArgument {
		t.Fatalf("want arg error, got %v", err)
	}
}

func TestAlignmentError(t *testing.T) {
	if got := AlignmentErrorMS(1_500_000, 0); got != 2 { // 1.5 ms rounds to 2
		t.Fatalf("rounding = %d", got)
	}
	if got := AlignmentErrorMS(999_000, 0); got != 1 {
		t.Fatalf("999us = %d ms", got)
	}
}

func TestReportRender(t *testing.T) {
	r := Report{
		TargetBPS: 1_800_000, ActualBPS: 1_805_000, BitrateDeviationPC: 27,
		QualitySamples: 108000, DroppedSamples: 0, AlignmentErrorMS: 1,
		PeakBufferBytes: 1_500_000, CacheState: "hit",
	}
	out := r.Render()
	for _, want := range []string{
		"target_bps=1800000",
		"bitrate_deviation_pct=0.27",
		"quality_samples=108000",
		"dropped_samples=0",
		"alignment_error_ms=1",
		"index_cache=hit",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("report missing %q:\n%s", want, out)
		}
	}
}
