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

// Stats are the recovery counters surfaced in the run summary.
type Stats struct {
	Recovered    int
	CacheMiss    int
	DupNACK      int
	LateFeedback int
}

// Recoverer processes NACK events against the cache.
type Recoverer struct {
	cache *Cache
	nacked map[uint64]bool
	Stats Stats
}

// NewRecoverer creates a Recoverer backed by cache.
func NewRecoverer(cache *Cache) *Recoverer {
	return &Recoverer{cache: cache, nacked: make(map[uint64]bool)}
}

// OnNACK resolves one NACK event. extSeqs are the requested extended
// sequence numbers; finalized reports whether the watermark has already
// finalized each sequence as lost (feedback arrived too late to act on
// the normal path and must go through replay instead — it is counted
// here and skipped).
//
// Returned events are copies reclassified as KindRecovered, in the order
// of extSeqs, so recovery is deterministic.
func (r *Recoverer) OnNACK(extSeqs []uint64, finalized func(seq uint64) bool) []*model.Event {
	var out []*model.Event
	for _, seq := range extSeqs {
		if r.nacked[seq] {
			r.Stats.DupNACK++
			continue
		}
		r.nacked[seq] = true
		if finalized(seq) {
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
		out = append(out, &cp)
	}
	return out
}
