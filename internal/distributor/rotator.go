// Package distributor simulates an in-process key distribution service.
// Keys are generated, rotated and expiry-checked entirely inside the
// process; no network or real KMS is contacted, and key material never
// leaves the process boundary.
package distributor

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"mediakey/internal/crypto"
	"mediakey/internal/model"
)

// DefaultTTL is the lifetime assigned to each generated key.
const DefaultTTL = time.Hour

// ErrUnknownKey is returned when a key id has never been issued.
var ErrUnknownKey = errors.New("unknown key id")

// keyEntry stores one versioned key plus its validity window.
type keyEntry struct {
	id        string
	material  []byte
	createdAt time.Time
	expiresAt time.Time
}

// Rotator hands out deterministic per-shard key assignments and tracks key
// lifetimes through an injectable clock. It is safe for concurrent use.
type Rotator struct {
	mu          sync.Mutex
	clock       model.Clock
	packageID   string
	rotateEvery int
	ttl         time.Duration
	entries     []*keyEntry
	byID        map[string]*keyEntry
	shardKeyID  map[int]string
	rotationCnt int
}

// New builds a Rotator. rotateEvery is the shard-index interval at which the
// key must change (a value <= 1 is clamped to 1).
func New(clock model.Clock, packageID string, rotateEvery int, ttl time.Duration) *Rotator {
	if rotateEvery < 1 {
		rotateEvery = 1
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Rotator{
		clock:       clock,
		packageID:   packageID,
		rotateEvery: rotateEvery,
		ttl:         ttl,
		byID:        map[string]*keyEntry{},
		shardKeyID:  map[int]string{},
	}
}

// KeyIDFor returns the stable key identifier for a shard index, creating the
// key on first use. Retries pass the same shard index and therefore reuse
// the original key without increasing the rotation count.
func (r *Rotator) KeyIDFor(shardIndex int) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if id, ok := r.shardKeyID[shardIndex]; ok {
		return id, nil
	}
	group := shardIndex / r.rotateEvery
	for len(r.entries) <= group {
		if err := r.rotateLocked(); err != nil {
			return "", err
		}
	}
	id := r.entries[group].id
	r.shardKeyID[shardIndex] = id
	return id, nil
}

// rotateLocked appends a fresh key. The first key does not count as a
// rotation; every subsequent switch does.
func (r *Rotator) rotateLocked() error {
	material := make([]byte, crypto.KeySize)
	if _, err := rand.Read(material); err != nil {
		return fmt.Errorf("key generation: %w", err)
	}
	now := r.clock.Now()
	seq := len(r.entries)
	mac := hmac.New(sha256.New, material)
	mac.Write([]byte(r.packageID))
	var seqBuf [8]byte
	for i := 0; i < 8; i++ {
		seqBuf[7-i] = byte(seq >> (8 * i))
	}
	mac.Write(seqBuf[:])
	id := "key-" + hex.EncodeToString(mac.Sum(nil))[:16]
	entry := &keyEntry{
		id:        id,
		material:  material,
		createdAt: now,
		expiresAt: now.Add(r.ttl),
	}
	r.entries = append(r.entries, entry)
	r.byID[id] = entry
	if seq > 0 {
		r.rotationCnt++
	}
	return nil
}

// Material returns the raw key bytes for an id. Key material must never be
// rendered into reports or logs.
func (r *Rotator) Material(id string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[id]
	if !ok {
		return nil, ErrUnknownKey
	}
	return append([]byte(nil), e.material...), nil
}

// IsExpiredAt reports whether the key is unknown or expired at an arbitrary
// point in time, used by delayed shards that carry their own clock.
func (r *Rotator) IsExpiredAt(id string, at model.Clock) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[id]
	if !ok {
		return true
	}
	return !at.Now().Before(e.expiresAt)
}

// IsExpired reports whether the key is unknown or past its expiry at the
// given clock's current time.
func (r *Rotator) IsExpired(id string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.byID[id]
	if !ok {
		return true
	}
	return !r.clock.Now().Before(e.expiresAt)
}

// RotationCount returns the number of key switches after the initial key.
func (r *Rotator) RotationCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rotationCnt
}

// Count returns how many distinct keys have been issued.
func (r *Rotator) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}
