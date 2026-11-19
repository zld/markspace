package pipeline

import (
	"sync"
	"time"
)

// Clock is an injectable, monotonic-offset wall clock. Packaging and
// validation read the same clock, so advancing it moves both license and key
// expiry boundaries together exactly as a real deployment would experience.
//
// Production runs anchor the clock at time.Now and never advance it during a
// validation. Seeded runs anchor it at a fixed instant so boundary tests are
// reproducible.
type Clock struct {
	mu    sync.Mutex
	base  time.Time
	delta time.Duration
}

// NewClock returns a clock anchored at base.
func NewClock(base time.Time) *Clock {
	return &Clock{base: base.UTC()}
}

// SystemClock returns a clock anchored at the current system time.
func SystemClock() *Clock { return NewClock(time.Now()) }

// Now returns the current injected time.
func (c *Clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.base.Add(c.delta)
}

// Advance moves the clock forward by d and returns the new now. A negative
// duration is ignored.
func (c *Clock) Advance(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d > 0 {
		c.delta += d
	}
	return c.base.Add(c.delta)
}

// SetOffset sets the total advancement to d and returns the resulting now.
// It exists so a boundary probe can advance and then restore the clock.
func (c *Clock) SetOffset(d time.Duration) time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.delta = d
	return c.base.Add(c.delta)
}

// Offset returns the total advancement applied so far.
func (c *Clock) Offset() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.delta
}
