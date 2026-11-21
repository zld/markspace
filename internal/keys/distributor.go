// Package keys generates, rotates and looks up in-process AES-256 data
// keys. Key material never leaves the process and is never serialized.
package keys

import (
	"crypto/rand"
	"fmt"
	"sync"
	"time"

	"mediaseal/internal/media"
)

const keyLifetime = time.Hour

type entry struct {
	info     media.KeyInfo
	material []byte
}

// Distributor issues a fresh AES-256 key every rotateEvery segments and
// hands out key material only for in-process decryption.
type Distributor struct {
	mu          sync.Mutex
	rotateEvery int
	clock       media.Clock
	entries     []*entry
}

// NewDistributor returns a Distributor rotating keys every rotateEvery
// segments (values below 1 are clamped to 1).
func NewDistributor(rotateEvery int, clock media.Clock) *Distributor {
	if rotateEvery < 1 {
		rotateEvery = 1
	}
	return &Distributor{rotateEvery: rotateEvery, clock: clock}
}

// KeyForSegment returns the key info for the epoch covering index, rotating
// in new keys as needed.
func (d *Distributor) KeyForSegment(index int) (media.KeyInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	epoch := index / d.rotateEvery
	for len(d.entries) <= epoch {
		if err := d.rotateLocked(); err != nil {
			return media.KeyInfo{}, err
		}
	}
	return d.entries[epoch].info, nil
}

func (d *Distributor) rotateLocked() error {
	material := make([]byte, 32)
	if _, err := rand.Read(material); err != nil {
		return fmt.Errorf("generate data key: %w", err)
	}
	now := d.clock.Now()
	epoch := len(d.entries)
	d.entries = append(d.entries, &entry{
		info: media.KeyInfo{
			KeyID:     fmt.Sprintf("key-%04d", epoch),
			Epoch:     epoch,
			NotBefore: now,
			NotAfter:  now.Add(keyLifetime),
		},
		material: material,
	})
	return nil
}

// Lookup returns the key material and info for keyID.
func (d *Distributor) Lookup(keyID string) ([]byte, media.KeyInfo, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, e := range d.entries {
		if e.info.KeyID == keyID {
			return e.material, e.info, true
		}
	}
	return nil, media.KeyInfo{}, false
}

// ExpireKeyForSegment forces the key covering index to be already expired,
// which affects every segment of that key's epoch.
func (d *Distributor) ExpireKeyForSegment(index int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	epoch := index / d.rotateEvery
	if epoch < len(d.entries) {
		d.entries[epoch].info.NotAfter = d.clock.Now().Add(-time.Second)
	}
}

// Infos returns metadata for every issued key, without any key material.
func (d *Distributor) Infos() []media.KeyInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]media.KeyInfo, len(d.entries))
	for i, e := range d.entries {
		out[i] = e.info
	}
	return out
}

// RotationCount reports how many rotations happened (keys issued minus one).
func (d *Distributor) RotationCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.entries) == 0 {
		return 0
	}
	return len(d.entries) - 1
}
