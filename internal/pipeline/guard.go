package pipeline

import (
	"errors"
	"sync"
)

// MemoryGuard bounds how many packets may be buffered across the pipeline at
// once. It complements the per-queue capacity: queues bound each inter-stage
// hop, the guard bounds the total in-flight work so memory stays a function
// of configuration rather than input size.
type MemoryGuard struct {
	mu         sync.Mutex
	maxPackets int
	inFlight   int
}

// ErrBufferBudget is returned when admitting an item would exceed the
// configured maximum packet buffer.
var ErrBufferBudget = errors.New("maximum TS packet buffer exceeded")

// NewMemoryGuard creates a guard with the given packet budget (>= 1).
func NewMemoryGuard(maxPackets int) *MemoryGuard {
	if maxPackets < 1 {
		maxPackets = 1
	}
	return &MemoryGuard{maxPackets: maxPackets}
}

// Acquire reserves one buffer slot. It returns false instead of blocking when
// the budget is exhausted; callers treat this as back-pressure and retry
// after a downstream stage releases a slot.
func (g *MemoryGuard) Acquire() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight >= g.maxPackets {
		return false
	}
	g.inFlight++
	return true
}

// Release returns one buffer slot.
func (g *MemoryGuard) Release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inFlight > 0 {
		g.inFlight--
	}
}

// InFlight returns the currently reserved slots.
func (g *MemoryGuard) InFlight() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.inFlight
}

// Limit returns the configured budget.
func (g *MemoryGuard) Limit() int { return g.maxPackets }
