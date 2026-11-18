// Package stats collects per-run receive-side statistics: packet counts
// and RFC 3550 interarrival jitter.
package stats

import (
	"sync"
	"time"
)

// Window accumulates receive statistics. It is safe for concurrent use by
// the receive loop and the summary printer.
type Window struct {
	mu          sync.Mutex
	clockRate   float64 // RTP clock rate, e.g. 90000 for video
	received    uint64
	jitter      float64 // RFC 3550 estimate, in timestamp units
	prevTransit float64
	started     bool
}

// NewWindow creates a Window for the given RTP clock rate.
func NewWindow(clockRate uint32) *Window {
	return &Window{clockRate: float64(clockRate)}
}

// Add records one received packet with its RTP timestamp and arrival time.
// Jitter follows RFC 3550 §6.4.1: J += (|D| - J) / 16, where D is the
// difference of packet transit times in timestamp units.
func (w *Window) Add(timestamp uint32, arrival time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()

	transit := float64(arrival.UnixNano())/1e9*w.clockRate - float64(timestamp)
	if w.started {
		d := transit - w.prevTransit
		if d < 0 {
			d = -d
		}
		w.jitter += (d - w.jitter) / 16
	}
	w.prevTransit = transit
	w.started = true
	w.received++
}

// Snapshot is a consistent read-out of the Window.
type Snapshot struct {
	Received uint64
	// JitterMs is the RFC 3550 interarrival jitter in milliseconds.
	// It is exactly 0 when fewer than two packets arrived.
	JitterMs float64
	// InsufficientSamples is true when fewer than two packets arrived and
	// the jitter figure is therefore not meaningful.
	InsufficientSamples bool
}

// Snapshot returns the current statistics.
func (w *Window) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := Snapshot{Received: w.received}
	if w.received < 2 {
		s.InsufficientSamples = true
		return s
	}
	s.JitterMs = w.jitter / w.clockRate * 1000
	return s
}
