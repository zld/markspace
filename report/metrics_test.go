package report

import (
	"math"
	"testing"
	"time"
)

func TestPercentileDurationNearestRank(t *testing.T) {
	values := []time.Duration{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := PercentileDuration(values, 0.95); got != 10 {
		t.Fatalf("P95 expected 10, got %d", got)
	}
	if got := PercentileDuration(values, 0.50); got != 5 {
		t.Fatalf("P50 expected 5, got %d", got)
	}
	if got := PercentileDuration(nil, 0.95); got != 0 {
		t.Fatalf("empty percentile should be zero, got %d", got)
	}
}

func TestCollectorSnapshotFiniteAndNonNegative(t *testing.T) {
	c := NewCollector()
	for i := 0; i < 100; i++ {
		c.ObserveLatency(time.Duration(i) * time.Microsecond)
	}
	c.SetQueue(8, 8)
	c.Update(func(m *Metrics) {
		m.AVSyncErrorNanos = 4_000_000
		m.AVSyncErrorMS = 4
		m.RecoveryRate = 1
	})
	m := c.Snapshot()
	if m.LatencySamples != 100 {
		t.Fatalf("latency samples not recorded: %+v", m)
	}
	if m.P95LatencyMS < 0 || math.IsNaN(m.P95LatencyMS) || math.IsInf(m.P95LatencyMS, 0) {
		t.Fatalf("P95 must be finite non-negative: %f", m.P95LatencyMS)
	}
	if m.PeakQueueOccupancy != 8 || m.QueueCapacity != 8 {
		t.Fatalf("queue metrics wrong: %+v", m)
	}
	if m.AVSyncErrorMS > 20 {
		t.Fatalf("sync error exceeds acceptance bound: %f", m.AVSyncErrorMS)
	}
}
