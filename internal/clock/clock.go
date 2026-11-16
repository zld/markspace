// Package clock provides a monotonic clock abstraction: a real clock for
// live UDP reception and a controllable virtual clock for reproducible
// simulations.
package clock

import (
	"sync"
	"time"
)

// Clock reports monotonic time since an arbitrary epoch.
type Clock interface {
	Now() time.Duration
	Sleep(d time.Duration)
}

// Real is backed by the monotonic component of time.Now.
type Real struct {
	start time.Time
}

func NewReal() *Real { return &Real{start: time.Now()} }

func (r *Real) Now() time.Duration { return time.Since(r.start) }

func (r *Real) Sleep(d time.Duration) { time.Sleep(d) }

// Virtual is a manually advanced clock for deterministic playback loops.
type Virtual struct {
	mu  sync.Mutex
	now time.Duration
}

func NewVirtual() *Virtual { return &Virtual{} }

func (v *Virtual) Now() time.Duration {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.now
}

// Set moves the clock to an absolute time; it never moves backwards.
func (v *Virtual) Set(t time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if t > v.now {
		v.now = t
	}
}

// Advance moves the clock forward by d.
func (v *Virtual) Advance(d time.Duration) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.now += d
}

// Sleep on a virtual clock simply advances it.
func (v *Virtual) Sleep(d time.Duration) { v.Advance(d) }
