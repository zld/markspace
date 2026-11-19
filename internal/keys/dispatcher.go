// Package keys implements the in-process key dispatcher: test root-key
// derivation, per-round key derivation, index-driven rotation, revocation and
// shutdown. All key material lives in memory and is never written to disk.
package keys

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sync"

	"mediakey/internal/manifest"
)

// Derivation labels provide domain separation so a key derived for one purpose
// can never be confused with another.
const (
	labelRoot  = "mediakey/test-root/v1"
	labelRound = "mediakey/key-round/v1/"
)

// DeriveTestRootKey derives a deterministic 32-byte test root key from a
// fixed seed. It is explicitly for local tests and demos and must never be
// used as a production key.
func DeriveTestRootKey(seed []byte) []byte {
	mac := hmac.New(sha256.New, seed)
	mac.Write([]byte(labelRoot))
	return mac.Sum(nil)
}

// KeyID returns the stable identifier embedded in envelopes and reports for
// the given round.
func KeyID(round int) string {
	return fmt.Sprintf("key-r%03d", round)
}

func deriveRoundKey(root []byte, round int) []byte {
	var num [4]byte
	binary.BigEndian.PutUint32(num[:], uint32(round))
	mac := hmac.New(sha256.New, root)
	mac.Write([]byte(labelRound))
	mac.Write(num[:])
	return mac.Sum(nil)
}

// Dispatcher hands out round keys and tracks rotation state. A single mutex
// guards every field so concurrent verification keeps rotation counts and
// activation state consistent.
type Dispatcher struct {
	mu            sync.Mutex
	root          []byte
	rotateEvery   int
	activeRound   int
	rotationCount int
	activated     map[int]bool
	revoked       map[int]bool
	closed        bool
}

// New builds a dispatcher whose round 0 is activated immediately. The initial
// round does not count as a rotation; only later, successfully activated
// rounds increment the counter.
func New(root []byte, rotateEvery int) (*Dispatcher, error) {
	if len(root) != sha256.Size {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, "root key must be 32 bytes")
	}
	if rotateEvery <= 0 {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, "rotate-every must be positive")
	}
	d := &Dispatcher{
		root:        append([]byte(nil), root...),
		rotateEvery: rotateEvery,
		activated:   map[int]bool{0: true},
		revoked:     map[int]bool{},
	}
	return d, nil
}

// RotationCount returns the number of successfully activated new rounds.
func (d *Dispatcher) RotationCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rotationCount
}

// ActiveRound returns the highest activated round.
func (d *Dispatcher) ActiveRound() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.activeRound
}

// RoundForIndex computes the round that owns chunk index idx under the
// configured rotation interval.
func (d *Dispatcher) RoundForIndex(idx int) (int, error) {
	if idx < 0 {
		return 0, manifest.NewError(manifest.ReasonInvalidInput, "negative chunk index")
	}
	round := idx / d.rotateEvery
	if round >= manifest.MaxKeyRounds {
		return 0, manifest.NewError(manifest.ReasonLimitExceeded, "key round limit exceeded")
	}
	return round, nil
}

// EnsureForIndex activates every round up to the one owning idx, in order.
// Re-activating an already active round is a no-op (and does not inflate the
// rotation count); only the first successful activation of a new round
// increments it.
func (d *Dispatcher) EnsureForIndex(idx int) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return 0, manifest.NewError(manifest.ReasonDispatcherClosed, "dispatcher is closed")
	}
	round, err := d.RoundForIndex(idx)
	if err != nil {
		return 0, err
	}
	for r := d.activeRound + 1; r <= round; r++ {
		if r >= manifest.MaxKeyRounds {
			return 0, manifest.NewError(manifest.ReasonLimitExceeded, "key round limit exceeded")
		}
		d.activated[r] = true
		d.activeRound = r
		d.rotationCount++ // counts only newly activated rounds
	}
	return round, nil
}

// KeyForRound returns the derived key and its ID for an activated round.
// Unknown, revoked and post-close requests each get a distinct error code.
func (d *Dispatcher) KeyForRound(round int) ([]byte, string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, "", manifest.NewError(manifest.ReasonDispatcherClosed, "dispatcher is closed")
	}
	if !d.activated[round] {
		return nil, "", manifest.NewError(manifest.ReasonUnknownRound, fmt.Sprintf("round %d not activated", round))
	}
	if d.revoked[round] {
		return nil, "", manifest.NewError(manifest.ReasonRevokedRound, fmt.Sprintf("round %d revoked", round))
	}
	return deriveRoundKey(d.root, round), KeyID(round), nil
}

// Revoke marks an activated round as revoked; future KeyForRound calls on it
// fail. Revocation never removes already-returned key material from callers.
func (d *Dispatcher) Revoke(round int) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return manifest.NewError(manifest.ReasonDispatcherClosed, "dispatcher is closed")
	}
	if !d.activated[round] {
		return manifest.NewError(manifest.ReasonUnknownRound, fmt.Sprintf("round %d not activated", round))
	}
	d.revoked[round] = true
	return nil
}

// Close shuts the dispatcher down. Subsequent key requests fail explicitly
// instead of silently deriving more keys.
func (d *Dispatcher) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.closed = true
	for i := range d.root {
		d.root[i] = 0
	}
	return nil
}
