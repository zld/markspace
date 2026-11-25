package report

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"sync"
	"time"
)

// Metrics is the quantitative evidence printed by the terminal demo.
type Metrics struct {
	AVSyncErrorNanos              int64   `json:"av_sync_error_nanos"`
	AVSyncErrorMS                 float64 `json:"av_sync_error_ms"`
	MatchedAVPairs                int     `json:"matched_av_pairs"`
	LossRecoveryRate              float64 `json:"loss_recovery_rate"`
	ReorderRecoveryRate           float64 `json:"reorder_recovery_rate"`
	RecoveryRate                  float64 `json:"recovery_rate"`
	UnrecoveredPackets            int     `json:"unrecovered_packets"`
	DuplicatePackets              int     `json:"duplicate_packets"`
	P95LatencyNanos               int64   `json:"p95_latency_nanos"`
	P95LatencyMS                  float64 `json:"p95_latency_ms"`
	LatencySamples                int     `json:"latency_samples"`
	PeakQueueOccupancy            int     `json:"peak_queue_occupancy"`
	QueueCapacity                 int     `json:"queue_capacity"`
	SegmentCount                  int     `json:"segment_count"`
	SegmentCountPerRepresentation []int   `json:"segment_count_per_representation"`
	RepresentationCount           int     `json:"representation_count"`
	EventCount                    int     `json:"event_count"`
	BoundaryCount                 int     `json:"boundary_count"`
	DelayedBoundaryCount          int     `json:"delayed_boundary_count"`
}

// Collector is a concurrency-safe metrics accumulator.
type Collector struct {
	mu        sync.Mutex
	latencies []time.Duration
	metrics   Metrics
}

// NewCollector creates a collector.
func NewCollector() *Collector { return &Collector{} }

// ObserveLatency records one pipeline latency sample.
func (c *Collector) ObserveLatency(d time.Duration) {
	if d < 0 {
		d = 0
	}
	c.mu.Lock()
	c.latencies = append(c.latencies, d)
	c.metrics.LatencySamples = len(c.latencies)
	c.mu.Unlock()
}

// SetQueue updates queue capacity and peak occupancy.
func (c *Collector) SetQueue(capacity, peak int) {
	c.mu.Lock()
	c.metrics.QueueCapacity = capacity
	c.metrics.PeakQueueOccupancy = peak
	c.mu.Unlock()
}

// Update applies a function under the collector lock.
func (c *Collector) Update(fn func(*Metrics)) {
	c.mu.Lock()
	fn(&c.metrics)
	c.mu.Unlock()
}

// Snapshot computes percentiles and returns a value copy.
func (c *Collector) Snapshot() Metrics {
	c.mu.Lock()
	samples := append([]time.Duration(nil), c.latencies...)
	m := c.metrics
	c.mu.Unlock()

	p95 := PercentileDuration(samples, 0.95)
	m.P95LatencyNanos = int64(p95)
	m.P95LatencyMS = float64(p95) / float64(time.Millisecond)
	m.AVSyncErrorMS = float64(m.AVSyncErrorNanos) / float64(time.Millisecond)

	// Guard the JSON contract: these values must always be finite/non-negative.
	if math.IsNaN(m.P95LatencyMS) || math.IsInf(m.P95LatencyMS, 0) || m.P95LatencyMS < 0 {
		m.P95LatencyMS = 0
		m.P95LatencyNanos = 0
	}
	if math.IsNaN(m.AVSyncErrorMS) || math.IsInf(m.AVSyncErrorMS, 0) || m.AVSyncErrorMS < 0 {
		m.AVSyncErrorMS = 0
	}
	if math.IsNaN(m.RecoveryRate) || math.IsInf(m.RecoveryRate, 0) {
		m.RecoveryRate = 0
	}
	return m
}

// PercentileDuration returns the nearest-rank p percentile.
func PercentileDuration(values []time.Duration, p float64) time.Duration {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]time.Duration(nil), values...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	rank := int(math.Ceil(p*float64(len(sorted)))) - 1
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// PercentileInt64 is the integer-nanosecond equivalent used by deterministic
// generated latency fixtures and tests.
func PercentileInt64(values []int64, p float64) int64 {
	ds := make([]time.Duration, len(values))
	for i, v := range values {
		if v < 0 {
			v = 0
		}
		ds[i] = time.Duration(v)
	}
	return int64(PercentileDuration(ds, p))
}

// WriteJSON prints an indented JSON report to stdout.
func WriteJSON(v interface{}) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
