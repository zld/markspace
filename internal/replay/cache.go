// Package replay tracks verification-request nonces and monotonic sequence
// numbers. It enforces three guarantees: a nonce is accepted at most once,
// sequence numbers never move backwards, and entries evicted for capacity are
// not silently re-accepted — they remain tombstones until their time window
// expires.
package replay

import (
	"sync"
	"time"

	"mediakey/internal/manifest"
)

type entry struct {
	seenAt   int64 // unix seconds
	lastSeq  int64
	insertID uint64 // LRU ordering, monotonically increasing
}

// Cache is a bounded nonce/sequence cache with a time window.
type Cache struct {
	mu       sync.Mutex
	capacity int
	window   time.Duration
	live     map[string]*entry
	dead     map[string]*entry // tombstones for evicted nonces
	nextID   uint64
	maxSeq   int64 // highest sequence ever accepted
}

// New builds a cache. Capacity and window are validated against the manifest
// safety limits so callers cannot request unbounded state.
func New(capacity int, window time.Duration) (*Cache, error) {
	if capacity <= 0 || capacity > manifest.MaxCacheEntries {
		return nil, manifest.NewError(manifest.ReasonLimitExceeded, "cache capacity out of allowed range")
	}
	if window <= 0 {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, "replay window must be positive")
	}
	return &Cache{
		capacity: capacity,
		window:   window,
		live:     make(map[string]*entry),
		dead:     make(map[string]*entry),
		maxSeq:   -1,
	}, nil
}

// Check registers a request identified by nonce with an increasing sequence
// number. It returns nil exactly once per nonce within the window.
//
//	duplicate live nonce, seq regressed -> sequence_out_of_order
//	duplicate live nonce, seq advanced  -> replay
//	evicted nonce, window not passed     -> window_outside (never auto-allowed)
//	evicted nonce, window passed         -> accepted as a fresh request
//	unknown nonce with stale seq         -> sequence_out_of_order
func (c *Cache) Check(nonce string, seq int64, now time.Time) error {
	if nonce == "" {
		return manifest.NewError(manifest.ReasonInvalidInput, "empty nonce")
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	ts := now.Unix()
	if e, ok := c.live[nonce]; ok {
		if seq < e.lastSeq {
			return manifest.NewError(manifest.ReasonOutOfOrder, "sequence number regression")
		}
		return manifest.NewError(manifest.ReasonReplay, "nonce already seen")
	}
	if e, ok := c.dead[nonce]; ok {
		if ts-e.seenAt <= int64(c.window/time.Second) {
			return manifest.NewError(manifest.ReasonWindowOutside, "nonce evicted but still inside replay window")
		}
		// Window elapsed: the nonce may legitimately be reused.
		delete(c.dead, nonce)
	}
	if seq <= c.maxSeq {
		return manifest.NewError(manifest.ReasonOutOfOrder, "sequence number regression")
	}
	c.addLocked(nonce, ts, seq)
	c.maxSeq = seq
	return nil
}

func (c *Cache) addLocked(nonce string, ts int64, seq int64) {
	if len(c.live) >= c.capacity {
		c.evictLocked(ts)
	}
	c.nextID++
	c.live[nonce] = &entry{seenAt: ts, lastSeq: seq, insertID: c.nextID}
}

// evictLocked removes the oldest live entry (LRU by insertID). Expired
// tombstones are purged first; the evicted nonce becomes a tombstone so a
// replayed copy cannot pass before its window closes.
func (c *Cache) evictLocked(nowTS int64) {
	for n, e := range c.dead {
		if nowTS-e.seenAt > int64(c.window/time.Second) {
			delete(c.dead, n)
		}
	}
	if len(c.dead) >= c.capacity {
		// Bound tombstone memory as well: drop the oldest tombstone.
		var oldest string
		var oldestID uint64 = ^uint64(0)
		for n, e := range c.dead {
			if e.insertID < oldestID {
				oldestID, oldest = e.insertID, n
			}
		}
		delete(c.dead, oldest)
	}
	var victim string
	var oldestID uint64 = ^uint64(0)
	for n, e := range c.live {
		if e.insertID < oldestID {
			oldestID, victim = e.insertID, n
		}
	}
	c.dead[victim] = c.live[victim]
	delete(c.live, victim)
}

// Size reports current live and tombstone counts (used by tests).
func (c *Cache) Size() (live, tombstones int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.live), len(c.dead)
}
