package clock

import "fmt"

// DriftCorrector estimates the relative speed of a remote clock against
// the local reference clock by least-squares fitting over a sliding
// window of (remote, local) observation pairs, and maps remote
// timestamps onto the local timeline. It never extrapolates from average
// rates alone: every correction uses the fitted window.
type DriftCorrector struct {
	window int
	remote []int64 // ring buffer of remote ticks
	local  []int64 // ring buffer of matching local nanoseconds
}

// NewDriftCorrector creates a corrector fitting over the last window
// samples. window must be >= 2.
func NewDriftCorrector(window int) (*DriftCorrector, error) {
	if window < 2 {
		return nil, fmt.Errorf("drift window must be >= 2, got %d", window)
	}
	return &DriftCorrector{window: window}, nil
}

// Add records a paired observation: remote tick value and the local
// nanosecond time at which it was observed.
func (d *DriftCorrector) Add(remote, local int64) {
	d.remote = append(d.remote, remote)
	d.local = append(d.local, local)
	if len(d.remote) > d.window {
		d.remote = d.remote[1:]
		d.local = d.local[1:]
	}
}

// Slope returns the fitted local-nanoseconds-per-remote-tick ratio and
// whether enough samples exist to fit.
func (d *DriftCorrector) Slope() (float64, bool) {
	n := len(d.remote)
	if n < 2 {
		return 0, false
	}
	var mx, my float64
	for i := 0; i < n; i++ {
		mx += float64(d.remote[i])
		my += float64(d.local[i])
	}
	mx /= float64(n)
	my /= float64(n)
	var sxy, sxx float64
	for i := 0; i < n; i++ {
		dx := float64(d.remote[i]) - mx
		sxy += dx * (float64(d.local[i]) - my)
		sxx += dx * dx
	}
	if sxx == 0 {
		return 0, false
	}
	return sxy / sxx, true
}

// Correct maps a remote tick onto the local timeline (nanoseconds)
// using the current window fit. Before any fit is possible it falls
// back to the offset of the first observation.
func (d *DriftCorrector) Correct(remote int64) int64 {
	slope, ok := d.Slope()
	if !ok {
		if len(d.remote) == 0 {
			return 0
		}
		return d.local[0] + (remote - d.remote[0])
	}
	var mx, my float64
	for i := range d.remote {
		mx += float64(d.remote[i])
		my += float64(d.local[i])
	}
	n := float64(len(d.remote))
	mx /= n
	my /= n
	return int64(my + slope*(float64(remote)-mx))
}
