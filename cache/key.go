// Package cache is a content-addressed result store. A hit is never
// trusted on its key alone: the caller's source identity and content
// digest are re-verified against the stored entry, and any mismatch
// quarantines the entry and forces recomputation.
package cache

import (
	"crypto/sha256"

	"mediacore/media"
)

// SourceID identifies the origin of a cached artifact (capture device,
// session, pipeline revision). Two sources may not share results even
// for identical content.
type SourceID string

// Entry is one stored artifact with its provenance.
type Entry struct {
	Source  SourceID
	Digest  [sha256.Size]byte // digest of Payload as stored
	Payload []byte
}

// Key is the content address of a payload.
func Key(payload []byte) [sha256.Size]byte {
	return sha256.Sum256(payload)
}

// Cache stores entries by content key. It is not safe for concurrent
// use; the pipeline is single-threaded per stream.
type Cache struct {
	store      map[[sha256.Size]byte]Entry
	quarantine []Entry
	Hits       int
	Misses     int
}

// New builds an empty cache.
func New() *Cache {
	return &Cache{store: make(map[[sha256.Size]byte]Entry)}
}

// Put stores payload under its content key with its source identity.
// Returns the key.
func (c *Cache) Put(source SourceID, payload []byte) [sha256.Size]byte {
	k := Key(payload)
	c.PutFor(k, source, payload)
	return k
}

// PutFor stores payload under an arbitrary key (e.g. the digest of the
// input a result was computed from). The entry's own digest is always
// the payload's, so Get still verifies content integrity.
func (c *Cache) PutFor(key [sha256.Size]byte, source SourceID, payload []byte) {
	cp := make([]byte, len(payload))
	copy(cp, payload)
	c.store[key] = Entry{Source: source, Digest: Key(payload), Payload: cp}
}

// Get looks up a payload by content key and verifies both the source
// identity and the stored payload's digest before trusting the hit.
// A mismatch quarantines the entry and returns a classified error so
// the caller recomputes instead of consuming a forged hit.
func (c *Cache) Get(source SourceID, key [sha256.Size]byte) ([]byte, error) {
	const op = "cache.Get"
	e, ok := c.store[key]
	if !ok {
		c.Misses++
		return nil, nil
	}
	if e.Source != source {
		c.evict(key)
		return nil, media.Fault(media.CodeCacheSource, op, -1,
			"entry source %q does not match %q", e.Source, source)
	}
	if Key(e.Payload) != e.Digest {
		c.evict(key)
		return nil, media.Fault(media.CodeCacheDigest, op, -1,
			"stored payload fails digest verification")
	}
	c.Hits++
	return e.Payload, nil
}

// evict isolates a forged or corrupt entry: it leaves the serving map
// and is preserved separately for audit.
func (c *Cache) evict(key [sha256.Size]byte) {
	e := c.store[key]
	delete(c.store, key)
	c.quarantine = append(c.quarantine, e)
	c.Misses++
}

// Quarantined returns the isolated entries in eviction order.
func (c *Cache) Quarantined() []Entry {
	out := make([]Entry, len(c.quarantine))
	copy(out, c.quarantine)
	return out
}

// Inject is a test/fault-injection hook: it writes an entry directly,
// bypassing Put's invariants, to simulate a poisoned store.
func (c *Cache) Inject(key [sha256.Size]byte, e Entry) {
	c.store[key] = e
}
