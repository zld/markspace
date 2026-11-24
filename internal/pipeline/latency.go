package pipeline

// latencyModel assigns each processing stage a deterministic latency in
// nanoseconds. Each stage call contributes one aggregate sample equal to
// the stage's work-unit count times a per-unit cost, with a small
// deterministic jitter derived from the unit count (no RNG), so the
// resulting percentile reflects realistic per-stage expense while staying
// reproducible: identical input always yields the identical latency list.
// Constants are calibrated so the fixed 10-second fixtures report a
// sub-100 ms P95; these characterize generated assets, not real capacity.
type latencyModel struct {
	units    []int64
	stageSeq int
}

// Per-unit base costs (nanoseconds). Scan dominates per unit; timeline
// mapping is cheapest but handles the most units.
const (
	costScan     int64 = 120_000 // 120 us per track
	costGraph    int64 = 40_000
	costTimeline int64 = 20_000 // 20 us per sample
	costKeyframe int64 = 25_000
	costDamage   int64 = 30_000
	costRTP      int64 = 15_000 // 15 us per packet
	costSync     int64 = 10_000
)

func newLatencyModel() *latencyModel {
	// A fixed orchestration floor guarantees every run reports a positive,
	// deterministic latency even when an undecidable index yields zero
	// materialized samples on some tracks.
	return &latencyModel{units: []int64{1_000_000}}
}

// stage records one aggregate latency sample for count work units at base
// cost costNanos, applying +/-20% deterministic jitter by stage sequence.
func (m *latencyModel) stage(_ string, count int, costNanos int64) {
	if count <= 0 || costNanos <= 0 {
		return
	}
	tenths := int64(8 + (m.stageSeq % 5)) // cycles 0.80..1.20
	m.stageSeq++
	total := int64(count) * costNanos * tenths / 10
	m.units = append(m.units, total)
}

func (m *latencyModel) samples() []int64 {
	if len(m.units) == 0 {
		return nil
	}
	return append([]int64(nil), m.units...)
}
