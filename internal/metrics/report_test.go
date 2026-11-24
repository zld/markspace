package metrics_test

import (
	"math"
	"testing"

	"mediapack/internal/metrics"
)

func TestBitrateFromMediaDuration(t *testing.T) {
	// 1500 bytes * 8 / 0.025s = 480 kbps.
	got := metrics.BitrateBps(1500, 0.025)
	if math.Abs(got-480000) > 1e-9 {
		t.Fatalf("bitrate=%v", got)
	}
	if metrics.BitrateBps(100, 0) != 0 {
		t.Fatal("zero duration must not produce a rate")
	}
}

func TestThresholds(t *testing.T) {
	good := &metrics.Report{BitrateDeviationPct: 4.9, ManifestAlignmentErrorMS: 0.9}
	if err := metrics.ThresholdError(good); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*metrics.Report{
		{PayloadHashMismatches: 1},
		{QualityDroppedSamples: 1},
		{ManifestAlignmentErrorMS: 1.1},
		{BitrateDeviationPct: 5.1, TargetBitrateBps: 100},
	} {
		if err := metrics.ThresholdError(bad); err == nil {
			t.Fatalf("expected threshold failure for %+v", bad)
		}
	}
}
