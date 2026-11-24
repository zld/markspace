package fault

import "sync"

// Collector is a concurrency-safe event sink. The zero value is ready to use.
type Collector struct {
	mu     sync.Mutex
	events []Event
}

// Add appends one event.
func (c *Collector) Add(e Event) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.events = append(c.events, e)
	c.mu.Unlock()
}

// Events returns a copy of collected events.
func (c *Collector) Events() []Event {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]Event, len(c.events))
	copy(out, c.events)
	return out
}

// Count returns the number of events with the supplied code. An empty code
// counts all events.
func (c *Collector) Count(code Code) int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if code == "" || e.Code == code {
			n++
		}
	}
	return n
}

// GapCount counts continuity, sequence, and truncation gap events.
func (c *Collector) GapCount() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, e := range c.events {
		if IsGapCode(e.Code) {
			n++
		}
	}
	return n
}

// IsGapCode reports whether code represents a missing media unit or interval.
func IsGapCode(code Code) bool {
	switch code {
	case ErrContinuity, ErrTruncated:
		return true
	default:
		return false
	}
}

// HasCode reports whether a code has been observed.
func (c *Collector) HasCode(code Code) bool {
	return c.Count(code) > 0
}
