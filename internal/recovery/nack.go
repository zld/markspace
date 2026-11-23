// Package recovery answers RTCP NACK feedback from a bounded retransmit
// cache. It never fabricates samples: a cache miss, a repeated NACK and
// feedback arriving after the watermark has finalized a loss are each
// counted and produce no media.
package recovery

import (
	"mediad/internal/model"
)

// Cache is a bounded FIFO store of recently sent media packets, keyed by
// extended sequence number.
type Cache struct {
	cap   int
	items map[uint64]*model.Event
	order []uint64
}

// NewCache creates a cache holding at most capacity packets.
func NewCache(capacity int) *Cache {
	return &Cache{cap: capacity, items: make(map[uint64]*model.Event)}
}

// Add stores a packet, evicting the oldest entry when full.
func (c *Cache) Add(ev *model.Event) {
	if _, ok := c.items[ev.Seq]; ok {
		return
	}
	if c.cap > 0 && len(c.order) >= c.cap {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
	c.items[ev.Seq] = ev
	c.order = append(c.order, ev.Seq)
}

// Get returns the cached packet for an extended sequence number.
func (c *Cache) Get(seq uint64) (*model.Event, bool) {
	ev, ok := c.items[seq]
	return ev, ok
}

// LossState is the pipeline's view of a missing sequence number.
type LossState int

const (
	StatePending   LossState = iota // gap seen, declaration not yet due
	StateDeclared                   // loss declared, still inside late window
	StateFinalized                  // beyond the late window, permanent
)

// Stats are the recovery counters surfaced in the run summary.
type Stats struct {
	Recovered    int
	CacheMiss    int
	DupNACK      int
	LateFeedback int
}

// Recoverer processes NACK events against the cache.
type Recoverer struct {
	cache  *Cache
	nacked map[uint64]bool
	Stats  Stats
}

// NewRecoverer creates a Recoverer backed by cache.
func NewRecoverer(cache *Cache) *Recoverer {
	return &Recoverer{cache: cache, nacked: make(map[uint64]bool)}
}

// OnNACK resolves one NACK event. state reports the LossState of each
// requested extended sequence number. Sequences whose loss is finalized
// are counted as late feedback and skipped; declared losses are
// recovered into the late return value (the caller replays them);
// pending losses are recovered into the normal return value. A cache
// miss never fabricates a sample.
func (r *Recoverer) OnNACK(extSeqs []uint64, state func(seq uint64) LossState) (normal, late []*model.Event) {
	for _, seq := range extSeqs {
		if r.nacked[seq] {
			r.Stats.DupNACK++
			continue
		}
		r.nacked[seq] = true
		st := state(seq)
		if st == StateFinalized {
			r.Stats.LateFeedback++
			continue
		}
		ev, ok := r.cache.Get(seq)
		if !ok {
			r.Stats.CacheMiss++
			continue
		}
		r.Stats.Recovered++
		cp := *ev
		cp.Kind = model.KindRecovered
		if st == StateDeclared {
			late = append(late, &cp)
		} else {
			normal = append(normal, &cp)
		}
	}
	return normal, late
}
