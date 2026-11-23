// Package recovery answers RTCP NACK feedback from a bounded retransmit
// cache. It never fabricates samples: a cache miss, a repeated NACK and
// feedback arriving after the watermark has finalized a loss are each
// counted and produce no media.
package recovery

import (
	"mediad/internal/model"
)

// Key identifies a sender packet for retransmission on the wire domain.
// The wire sequence number is used instead of the receiver's extended
// number so the cache is independent of where the receiver joined the
// stream (and therefore of any wrap-origin difference).
type Key struct {
	SSRC  uint32
	Seq16 uint16
}

// Cache is a bounded FIFO store of recently sent media packets.
type Cache struct {
	cap   int
	items map[Key]*model.Event
	order []Key
}

// NewCache creates a cache holding at most capacity packets.
func NewCache(capacity int) *Cache {
	return &Cache{cap: capacity, items: make(map[Key]*model.Event)}
}

// Add stores a packet, evicting the oldest entry when full.
func (c *Cache) Add(ev *model.Event) {
	key := Key{SSRC: ev.SSRC, Seq16: ev.Seq16}
	if _, ok := c.items[key]; ok {
		return
	}
	if c.cap > 0 && len(c.order) >= c.cap {
		delete(c.items, c.order[0])
		c.order = c.order[1:]
	}
	c.items[key] = ev
	c.order = append(c.order, key)
}

// Get returns the cached packet for a wire key.
func (c *Cache) Get(ssrc uint32, seq16 uint16) (*model.Event, bool) {
	ev, ok := c.items[Key{SSRC: ssrc, Seq16: seq16}]
	return ev, ok
}

// Len reports the number of held packets; used for bounded-cache tests.
func (c *Cache) Len() int { return len(c.order) }

// LossState is the pipeline's view of a missing sequence number.
type LossState int

const (
	StatePending   LossState = iota // gap seen, declaration not yet due
	StateDeclared                   // loss declared, still inside late window
	StateFinalized                  // beyond the late window, permanent
	StatePresent                    // already delivered (stale feedback)
)

// Stats are the recovery counters surfaced in the run summary.
type Stats struct {
	Recovered    int
	CacheMiss    int
	DupNACK      int
	LateFeedback int
	// RecoveredKeys records which wire keys were restored, so the caller
	// can score recovery against the set the network actually dropped.
	RecoveredKeys map[Key]bool
}

// Request is one packet identifier taken from a NACK's PID/BLP list.
// Seq is the receiver's extended number, used to deduplicate feedback and
// to compare against the loss state.
type Request struct {
	Seq   uint64
	SSRC  uint32
	Seq16 uint16
}

// Recoverer processes NACK events against the cache.
type Recoverer struct {
	cache  *Cache
	nacked map[Key]bool
	Stats  Stats
}

// NewRecoverer creates a Recoverer backed by cache.
func NewRecoverer(cache *Cache) *Recoverer {
	return &Recoverer{
		cache:  cache,
		nacked: make(map[Key]bool),
		Stats:  Stats{RecoveredKeys: make(map[Key]bool)},
	}
}

// Offer places a sent packet into the bounded retransmit cache. It is
// called when the sender transmits, so eviction models cache lifetime
// rather than arrival order.
func (r *Recoverer) Offer(ev *model.Event) { r.cache.Add(ev) }

// OnNACK resolves one NACK event. state reports the LossState of each
// requested extended sequence number. Sequences whose loss is finalized
// are counted as late feedback and skipped; declared losses are
// recovered into the late return value (the caller replays them);
// pending losses are recovered into the normal return value. A cache
// miss never fabricates a sample.
func (r *Recoverer) OnNACK(reqs []Request, state func(seq uint64) LossState) (normal, late []*model.Event) {
	for _, req := range reqs {
		key := Key{SSRC: req.SSRC, Seq16: req.Seq16}
		if r.nacked[key] {
			r.Stats.DupNACK++
			continue
		}
		r.nacked[key] = true
		st := state(req.Seq)
		if st == StatePresent {
			continue // stale feedback for a packet already on the timeline
		}
		if st == StateFinalized {
			r.Stats.LateFeedback++
			continue
		}
		ev, ok := r.cache.Get(req.SSRC, req.Seq16)
		if !ok {
			r.Stats.CacheMiss++
			continue
		}
		r.Stats.Recovered++
		r.Stats.RecoveredKeys[key] = true
		cp := *ev
		cp.Kind = model.KindRecovered
		cp.Seq = req.Seq
		if st == StateDeclared {
			late = append(late, &cp)
		} else {
			normal = append(normal, &cp)
		}
	}
	return normal, late
}
