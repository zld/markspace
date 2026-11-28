package clock

// DriftEstimator estimates the constant offset and rate skew between a
// local clock (e.g. the container timeline) and a remote clock (e.g. an
// RTP packet stream). Both must be in the shared nanosecond base.
type DriftEstimator struct {
	n              int
	firstL, firstR int64
	lastL, lastR   int64
	minOff, maxOff int64
}

// Add records one (local, remote) observation in nanoseconds.
func (d *DriftEstimator) Add(local, remote int64) {
	off := remote - local
	if d.n == 0 {
		d.firstL, d.firstR = local, remote
		d.minOff, d.maxOff = off, off
	}
	d.lastL, d.lastR = local, remote
	if off < d.minOff {
		d.minOff = off
	}
	if off > d.maxOff {
		d.maxOff = off
	}
	d.n++
}

// Correction returns the estimated constant offset (remote-local) and the
// rate skew in PPM. ok is false when there is not enough data.
func (d *DriftEstimator) Correction() (offsetNanos int64, slopePPM float64, ok bool) {
	if d.n < 2 || d.lastL == d.firstL {
		return 0, 0, false
	}
	slope := float64(d.lastR-d.firstR)/float64(d.lastL-d.firstL) - 1.0
	return d.firstR - d.firstL, slope * 1e6, true
}

// Apply removes the estimated correction from a remote timestamp.
func (d *DriftEstimator) Apply(remote int64) int64 {
	off, slopePPM, ok := d.Correction()
	if !ok {
		return remote
	}
	span := float64(remote - d.firstR)
	return remote - off - int64(span*slopePPM/1e6)
}

// Bounds returns the min/max observed raw offset in nanoseconds.
func (d *DriftEstimator) Bounds() (minOff, maxOff int64, ok bool) {
	if d.n == 0 {
		return 0, 0, false
	}
	return d.minOff, d.maxOff, true
}
