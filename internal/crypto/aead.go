// Package crypto performs AES-256-GCM authenticated encryption of media
// shards. The additional authenticated data binds the package identifier,
// shard index, key identifier and manifest digest together, so any tampering
// with any of them is detected at decryption time.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"

	"mediakey/internal/model"
)

// KeySize is the AES-256 key length in bytes.
const KeySize = 32

// Sentinel errors so callers can map validation failures to fixed reasons
// without parsing error strings.
var (
	ErrManifestMismatch = errors.New("manifest digest mismatch")
	ErrAuthTag          = errors.New("ciphertext authentication failed")
	ErrLength           = errors.New("decrypted plaintext length mismatch")
	ErrBadKeySize       = errors.New("key must be 32 bytes for AES-256")
)

// Packet is the on-wire representation of one encrypted shard. Key material
// is never stored in a packet.
type Packet struct {
	ShardID      int    `json:"shard_id"`
	KeyID        string `json:"key_id"`
	Nonce        []byte `json:"nonce"`
	Ciphertext   []byte `json:"ciphertext"`
	BoundDigest  string `json:"bound_digest"`
	ManifestHint string `json:"manifest_hint,omitempty"`
}

// BuildAAD assembles the deterministic additional authenticated data. The
// same construction must be used for Seal and Open.
func BuildAAD(packageID, keyID, manifestDigest string, shardID int) []byte {
	out := make([]byte, 0, len(packageID)+len(keyID)+len(manifestDigest)+16)
	out = append(out, []byte(packageID)...)
	out = append(out, 0)
	var idBuf [8]byte
	binary.BigEndian.PutUint64(idBuf[:], uint64(shardID))
	out = append(out, idBuf[:]...)
	out = append(out, 0)
	out = append(out, []byte(keyID)...)
	out = append(out, 0)
	out = append(out, []byte(manifestDigest)...)
	return out
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, ErrBadKeySize
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("aes init: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm init: %w", err)
	}
	return aead, nil
}

// Seal encrypts plaintext with a fresh random nonce. Empty plaintext is a
// valid input and still produces an authenticated tag.
func Seal(packageID, keyID string, key []byte, shardID int, manifestDigest string, plaintext []byte) (*Packet, error) {
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("nonce generation: %w", err)
	}
	aad := BuildAAD(packageID, keyID, manifestDigest, shardID)
	sealed := aead.Seal(nil, nonce, plaintext, aad)
	return &Packet{
		ShardID:     shardID,
		KeyID:       keyID,
		Nonce:       nonce,
		Ciphertext:  sealed,
		BoundDigest: manifestDigest,
	}, nil
}

// Open authenticates and decrypts a packet. The manifest digest is verified
// explicitly before GCM verification so the two failure modes are reported
// separately. The decrypted length is checked against the manifest size.
// Callers must never retry Open with a newer key after a failure.
func Open(packageID string, key []byte, shardID int, manifestDigest string, expectedPlainLen int, p *Packet) ([]byte, string, error) {
	if p.BoundDigest != manifestDigest {
		return nil, model.ReasonManifest, ErrManifestMismatch
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, model.ReasonAuthTag, err
	}
	aad := BuildAAD(packageID, p.KeyID, manifestDigest, shardID)
	plain, err := aead.Open(nil, p.Nonce, p.Ciphertext, aad)
	if err != nil {
		return nil, model.ReasonAuthTag, ErrAuthTag
	}
	if len(plain) != expectedPlainLen {
		return nil, model.ReasonLength, ErrLength
	}
	return plain, model.ReasonNone, nil
}

// Corrupt flips one byte inside the encrypted payload while preserving the
// packet structure, deterministically driving Open into AUTH_TAG_MISMATCH.
// It never touches the nonce or the bound digest.
func Corrupt(p *Packet) {
	if len(p.Ciphertext) == 0 {
		// Empty shards only carry the GCM tag.
		p.Ciphertext = append(p.Ciphertext, 0x01)
		return
	}
	pos := int(p.ShardID) % len(p.Ciphertext)
	p.Ciphertext[pos] ^= 0xFF
}
