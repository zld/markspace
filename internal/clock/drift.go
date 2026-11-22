package clock

// DriftEstimator fits node-clock ticks against the canonical reference
// timeline with ordinary least squares over a fixed-size window. Observations
// are stored relative to the first pair, which keeps the fit well-conditioned
// for long streams and large absolute tick bases. The result is a pure
// function of the observed pairs, so a restored estimator reproduces identical
// ppm values after restart.
type DriftEstimator struct {
	capacity int
	ring     [][2]int64 // (reference units, node units) relative to first
	head     int
	filled   bool
	anchorX  uint64
	anchorY  uint64
	anchored bool
}

// NewDriftEstimator builds an estimator retaining the last capacity pairs.
func NewDriftEstimator(capacity int) *DriftEstimator {
	if capacity < 2 {
		capacity = 2
	}
	return &DriftEstimator{capacity: capacity, ring: make([][2]int64, capacity)}
}

// Observe records one aligned pair of reference and node values expressed in
// the same unit. The first pair defines the origin; later pairs are stored as
// differences, so both axes and the nominal slope are 1:1.
func (d *DriftEstimator) Observe(reference, node uint64) {
	if !d.anchored {
		d.anchorX, d.anchorY, d.anchored = reference, node, true
	}
	x := int64(reference - d.anchorX)
	y := int64(node - d.anchorY)
	d.ring[d.head] = [2]int64{x, y}
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
func (d *DriftEstimator) pair(i int) (int64, int64) {
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
	slope := (float64(n)*sumXY - sumX*sumY) / den // node units per reference unit
	return (slope - 1) * 1e6
}

// DriftSnapshot is the restartable estimator state.
type DriftSnapshot struct {
	Capacity int
	Ring     [][2]int64
	Head     int
	Filled   bool
	AnchorX  uint64
	AnchorY  uint64
	Anchored bool
}

// Snapshot captures estimator state.
func (d *DriftEstimator) Snapshot() DriftSnapshot {
	ring := make([][2]int64, d.capacity)
	copy(ring, d.ring)
	return DriftSnapshot{
		Capacity: d.capacity, Ring: ring, Head: d.head, Filled: d.filled,
		AnchorX: d.anchorX, AnchorY: d.anchorY, Anchored: d.anchored,
	}
}

// Restore rehydrates estimator state from a checkpoint.
func (d *DriftEstimator) Restore(s DriftSnapshot) {
	d.capacity = s.Capacity
	d.ring = make([][2]int64, s.Capacity)
	copy(d.ring, s.Ring)
	d.head, d.filled = s.Head, s.Filled
	d.anchorX, d.anchorY, d.anchored = s.AnchorX, s.AnchorY, s.Anchored
}
