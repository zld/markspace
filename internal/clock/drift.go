package clock

// DriftEstimator fits node-clock ticks against the canonical reference
// timeline with ordinary least squares over a fixed-size window. The result is
// a pure function of the observed pairs, so a restored estimator reproduces
// identical ppm values after restart.
type DriftEstimator struct {
	capacity int
	ring     [][2]uint64 // (reference nanos, node ticks) pairs
	head     int
	filled   bool
}

// NewDriftEstimator builds an estimator retaining the last capacity pairs.
func NewDriftEstimator(capacity int) *DriftEstimator {
	if capacity < 2 {
		capacity = 2
	}
	return &DriftEstimator{capacity: capacity, ring: make([][2]uint64, capacity)}
}

// Observe records one aligned pair of reference time and node ticks.
func (d *DriftEstimator) Observe(referenceNanos, nodeTicks uint64) {
	d.ring[d.head] = [2]uint64{referenceNanos, nodeTicks}
	d.head++
	if d.head == d.capacity {
		d.head = 0
		d.filled = true
	}
}

// Len reports the number of retained observations.
func (d *DriftEstimator) Len() int {
	if d.filled {
		return d.capacity
	}
	return d.head
}

// pair returns the i-th retained pair in insertion order.
func (d *DriftEstimator) pair(i int) (uint64, uint64) {
	start := 0
	if d.filled {
		start = d.head
	}
	p := d.ring[(start+i)%d.capacity]
	return p[0], p[1]
}

// PPM returns the node clock drift in parts-per-million relative to the
// reference clock: positive means the node clock runs fast. Zero is returned
// until at least two non-degenerate samples exist.
func (d *DriftEstimator) PPM() float64 {
	n := d.Len()
	if n < 2 {
		return 0
	}
	var sumX, sumY, sumXY, sumXX float64
	for i := 0; i < n; i++ {
		x, y := d.pair(i)
		xf, yf := float64(x), float64(y)
		sumX += xf
		sumY += yf
		sumXY += xf * yf
		sumXX += xf * xf
	}
	den := float64(n)*sumXX - sumX*sumX
	if den == 0 {
		return 0
	}
	slope := (float64(n)*sumXY - sumX*sumY) / den // node ticks per ns
	// Both axes are time units; drift ratio is slope minus the nominal 1:1.
	return (slope - 1) * 1e6
}

// DriftSnapshot is the restartable estimator state.
type DriftSnapshot struct {
	Capacity int
	Ring     [][2]uint64
	Head     int
	Filled   bool
}

// Snapshot captures estimator state.
func (d *DriftEstimator) Snapshot() DriftSnapshot {
	ring := make([][2]uint64, d.capacity)
	copy(ring, d.ring)
	return DriftSnapshot{Capacity: d.capacity, Ring: ring, Head: d.head, Filled: d.filled}
}

// Restore rehydrates estimator state from a checkpoint.
func (d *DriftEstimator) Restore(s DriftSnapshot) {
	d.capacity = s.Capacity
	d.ring = make([][2]uint64, s.Capacity)
	copy(d.ring, s.Ring)
	d.head, d.filled = s.Head, s.Filled
}
