// Package issuer holds key material for a single process. It hands out the
// current key version, rotates keys at segment boundaries, records rotation
// history and retires old versions after their validity window.
//
// All key bytes live only in memory. They are never serialized, logged or
// written to disk, and the only identifier that leaves this package is a
// version-scoped key id such as "k2".
package issuer

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"mediakey/internal/asset"
)

// Errors returned by the issuer.
var (
	ErrNoCurrentKey  = errors.New("issuer: no current key")
	ErrKeyNotFound   = errors.New("issuer: key version not found")
	ErrKeyExpired    = errors.New("issuer: key version expired")
	ErrRetiredKeyUse = errors.New("issuer: retired key cannot encrypt")
	ErrRotationLimit = errors.New("issuer: rotation limit reached")
)

// Key is a single AES-256 key version. Key bytes are unexported so that a Key
// value can be passed around without accidentally serializing its material.
type Key struct {
	Version   int
	ID        string
	CreatedAt time.Time
	ExpiresAt time.Time
	source    []byte
}

// Version returns the numeric key version, starting at 1.
func (k Key) VersionNumber() int { return k.Version }

// Expired reports whether t is at or after the key's expiry. A zero ExpiresAt
// means the key never expires.
func (k Key) Expired(t time.Time) bool {
	return !k.ExpiresAt.IsZero() && !t.Before(k.ExpiresAt)
}

// KeyID formats the stable identifier for version v.
func KeyID(v int) string { return fmt.Sprintf("k%d", v) }

// Options configures a Distributor.
type Options struct {
	// Now supplies the clock. Required.
	Now func() time.Time
	// Random supplies key material. Defaults to crypto/rand.Reader.
	Random io.Reader
	// KeyTTL is the validity window of every issued key, anchored at the
	// rotation time. Zero means keys never expire.
	KeyTTL time.Duration
	// MaxRotations caps the number of rotations; zero means unlimited. The
	// demo CLI caps this so a single run stays bounded.
	MaxRotations int
}

// Distributor is an in-process key distributor. It is safe for concurrent use.
type Distributor struct {
	now          func() time.Time
	random       io.Reader
	keyTTL       time.Duration
	maxRotations int

	mu       sync.Mutex
	current  int
	versions []Key // index i holds version i+1
}

// New creates a distributor with version 1 active immediately.
func New(opts Options) (*Distributor, error) {
	if opts.Now == nil {
		return nil, errors.New("issuer: clock is required")
	}
	if opts.MaxRotations < 0 {
		return nil, errors.New("issuer: negative rotation limit")
	}
	if opts.Random == nil {
		opts.Random = rand.Reader
	}
	d := &Distributor{
		now:          opts.Now,
		random:       opts.Random,
		keyTTL:       opts.KeyTTL,
		maxRotations: opts.MaxRotations,
	}
	k, err := d.issue(1)
	if err != nil {
		return nil, err
	}
	d.versions = append(d.versions, k)
	d.current = 1
	return d, nil
}

// issue generates a fresh key version anchored at the current clock reading.
func (d *Distributor) issue(version int) (Key, error) {
	buf := make([]byte, asset.KeySize)
	if _, err := io.ReadFull(d.random, buf); err != nil {
		return Key{}, fmt.Errorf("issuer: key material: %w", err)
	}
	now := d.now().UTC()
	k := Key{
		Version:   version,
		ID:        KeyID(version),
		CreatedAt: now,
		source:    buf,
	}
	if d.keyTTL > 0 {
		k.ExpiresAt = now.Add(d.keyTTL)
	}
	return k, nil
}

// Current returns the key currently used for new encryption.
func (d *Distributor) Current() Key {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.versions[d.current-1]
}

// Rotate retires the active key and activates the next version. It returns the
// newly active key and the number of rotations performed so far (the new
// version minus one). Rotation must happen on a segment boundary; the
// packaging pipeline is responsible for calling it only between segments.
func (d *Distributor) Rotate() (Key, int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	rotations := d.current - 1
	if d.maxRotations > 0 && rotations >= d.maxRotations {
		return Key{}, rotations, fmt.Errorf("%w: limit %d", ErrRotationLimit, d.maxRotations)
	}
	next := d.current + 1
	k, err := d.issue(next)
	if err != nil {
		return Key{}, rotations, err
	}
	d.versions = append(d.versions, k)
	d.current = next
	return k, next - 1, nil
}

// Rotations reports how many rotations have occurred.
func (d *Distributor) Rotations() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.current - 1
}

// Lookup resolves a key id to its key without any expiry policy. It is meant
// for diagnostics where the raw historical key is needed.
func (d *Distributor) Lookup(id string) (Key, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, k := range d.versions {
		if k.ID == id {
			return k, nil
		}
	}
	return Key{}, fmt.Errorf("%w: %s", ErrKeyNotFound, id)
}

// ResolveForDecrypt resolves a key id for decryption at time now. A version
// that exists but whose validity window has passed yields ErrKeyExpired; this
// error is terminal and callers must not fall back to another version.
func (d *Distributor) ResolveForDecrypt(id string, now time.Time) (Key, error) {
	k, err := d.Lookup(id)
	if err != nil {
		return Key{}, err
	}
	if k.Expired(now) {
		return Key{}, fmt.Errorf("%w: %s expired at %s", ErrKeyExpired, id, k.ExpiresAt.Format(time.RFC3339))
	}
	return k, nil
}

// ResolveForEncrypt returns the current key only if it is still valid at now.
// Retired and expired versions are never returned for encryption.
func (d *Distributor) ResolveForEncrypt(now time.Time) (Key, error) {
	d.mu.Lock()
	cur := d.versions[d.current-1]
	d.mu.Unlock()
	if cur.Expired(now) {
		return Key{}, fmt.Errorf("%w: %s", ErrKeyExpired, cur.ID)
	}
	return cur, nil
}

// History returns a diagnostic snapshot of every version ever issued,
// including retired and expired ones. Key material is deliberately omitted.
func (d *Distributor) History() []KeyInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]KeyInfo, len(d.versions))
	for i, k := range d.versions {
		out[i] = KeyInfo{
			Version:   k.Version,
			ID:        k.ID,
			CreatedAt: k.CreatedAt,
			ExpiresAt: k.ExpiresAt,
			Active:    k.Version == d.current,
		}
	}
	return out
}

// KeyInfo is a material-free description of a key version.
type KeyInfo struct {
	Version   int
	ID        string
	CreatedAt time.Time
	ExpiresAt time.Time
	Active    bool
}

// AEAD builds the AES-256-GCM cipher for this key. The returned AEAD shares
// no mutable state and is safe to use concurrently.
func (k Key) AEAD() (cipher.AEAD, error) {
	if len(k.source) != asset.KeySize {
		return nil, fmt.Errorf("issuer: key %s has no material", k.ID)
	}
	block, err := aes.NewCipher(k.source)
	if err != nil {
		return nil, fmt.Errorf("issuer: cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("issuer: gcm: %w", err)
	}
	return aead, nil
}
