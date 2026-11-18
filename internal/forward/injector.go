package forward

import (
	"context"
	"math/rand"
	"sync"
	"time"
)

// InjectConfig parameterises the deterministic fault model.
type InjectConfig struct {
	Seed          int64
	ReorderRate   float64 // fraction of packets swapped with their successor
	BurstEvery    int     // a burst delay fires every N packets (<=1 disables)
	BurstDelay    int64   // delay duration in milliseconds during a burst
	DuplicateRate float64 // fraction of packets emitted a second time
}

// Injector applies a reproducible stream of faults. The same seed always
// yields the same sequence of decisions, so races can be replayed.
type Injector struct {
	mu  sync.Mutex
	rng *rand.Rand
	cfg InjectConfig
	n   int64 // packets observed
	cnt InjectorCounters
}

// InjectorCounters counts faults as they are planned, independent of any
// downstream deduplication (e.g. SRTP replay protection).
type InjectorCounters struct {
	Delays     int64 `json:"burst_delays"`
	Duplicates int64 `json:"duplicates_planned"`
	Reorders   int64 `json:"reorders_planned"`
}

// NewInjector builds an injector from the supplied configuration.
func NewInjector(cfg InjectConfig) *Injector {
	return &Injector{rng: rand.New(rand.NewSource(cfg.Seed)), cfg: cfg}
}

// Decision describes what the stream driver should do with one packet.
type Decision struct {
	DelayMS   int64 // extra delay before emitting (0 for normal timing)
	Duplicate bool  // emit the packet twice
	Reorder   bool  // swap emission order with the next packet
}

// Plan returns the deterministic decision for the next packet.
func (g *Injector) Plan() Decision {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.n++
	d := Decision{}
	if g.cfg.BurstEvery > 1 && g.n%int64(g.cfg.BurstEvery) == 0 {
		d.DelayMS = g.cfg.BurstDelay
		g.cnt.Delays++
	}
	if g.cfg.ReorderRate > 0 && g.rng.Float64() < g.cfg.ReorderRate {
		d.Reorder = true
		g.cnt.Reorders++
	}
	if g.cfg.DuplicateRate > 0 && g.rng.Float64() < g.cfg.DuplicateRate {
		d.Duplicate = true
		g.cnt.Duplicates++
	}
	return d
}

// Counters returns a snapshot of the planned fault counts.
func (g *Injector) Counters() InjectorCounters {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.cnt
}

// Sleep waits for the decision's delay, returning early on cancellation.
func (d Decision) Sleep(ctx context.Context) error {
	if d.DelayMS <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(time.Duration(d.DelayMS) * time.Millisecond)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
