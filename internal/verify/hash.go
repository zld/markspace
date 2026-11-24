// Package verify computes the SHA-256 of the recovered output. The hash
// state is serialisable so a checkpointed run can resume and still produce
// the identical final digest.
package verify

import (
	"crypto/sha256"
	"encoding"
	"encoding/hex"
	"fmt"
	"hash"
)

// Hasher is a resumable SHA-256 over the output byte stream.
type Hasher struct {
	h hash.Hash
}

func New() *Hasher { return &Hasher{h: sha256.New()} }

// Write feeds output bytes into the digest.
func (h *Hasher) Write(p []byte) (int, error) { return h.h.Write(p) }

// SumHex returns the current digest as hex.
func (h *Hasher) SumHex() string { return hex.EncodeToString(h.h.Sum(nil)) }

// MarshalState serialises the in-progress hash state for a checkpoint.
func (h *Hasher) MarshalState() ([]byte, error) {
	m, ok := h.h.(encoding.BinaryMarshaler)
	if !ok {
		return nil, fmt.Errorf("hash state is not serialisable")
	}
	return m.MarshalBinary()
}

// UnmarshalState restores a Hasher from a checkpointed state.
func UnmarshalState(state []byte) (*Hasher, error) {
	h := &Hasher{h: sha256.New()}
	u, ok := h.h.(encoding.BinaryUnmarshaler)
	if !ok {
		return nil, fmt.Errorf("hash state is not restorable")
	}
	if err := u.UnmarshalBinary(state); err != nil {
		return nil, fmt.Errorf("restore hash state: %w", err)
	}
	return h, nil
}
