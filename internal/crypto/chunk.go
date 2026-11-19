// Package crypto implements AES-256-GCM chunk encryption.
//
// Each chunk is sealed independently. The GCM additional authenticated data
// (AAD) binds the asset ID, chunk index and key ID, so moving a ciphertext
// between chunks, assets or key rounds fails authentication rather than
// decrypting silently.
package crypto

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"io"

	"mediakey/internal/manifest"
)

// aadPrefix is the domain-separation tag for the AAD layout. Bumping the
// envelope format version requires a new prefix.
const aadPrefix = "MK1"

// KeySize is the required AES-256 key length in bytes.
const KeySize = 32

// DeriveAAD serializes the authenticated metadata. Length prefixes make the
// encoding unambiguous even if IDs contain arbitrary bytes.
func DeriveAAD(assetID string, chunkIndex int, keyID string) []byte {
	aad := make([]byte, 0, len(aadPrefix)+4+len(assetID)+4+4+len(keyID))
	aad = append(aad, aadPrefix...)
	aad = appendU32(aad, uint32(len(assetID)))
	aad = append(aad, assetID...)
	aad = appendU32(aad, uint32(chunkIndex))
	aad = appendU32(aad, uint32(len(keyID)))
	aad = append(aad, keyID...)
	return aad
}

func appendU32(buf []byte, v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return append(buf, b[:]...)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != KeySize {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, "key must be 32 bytes")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, err.Error())
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, err.Error())
	}
	return gcm, nil
}

// Encrypt seals plaintext under key with a fresh random nonce drawn from rand
// (a process-local crypto/rand source in production). The returned envelope
// carries the full format header; AAD binds the metadata.
func Encrypt(rand io.Reader, key []byte, assetID string, chunkIndex int, keyID string, plaintext []byte) (*manifest.Envelope, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if assetID == "" || keyID == "" || chunkIndex < 0 {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, "missing envelope metadata")
	}
	if len(plaintext) > manifest.MaxChunkBytes {
		return nil, manifest.NewError(manifest.ReasonLimitExceeded, "chunk plaintext too large")
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand, nonce); err != nil {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, "nonce generation failed: "+err.Error())
	}
	aad := DeriveAAD(assetID, chunkIndex, keyID)
	sealed := gcm.Seal(nil, nonce, plaintext, aad)
	return &manifest.Envelope{
		Version:    manifest.FormatVersion,
		AssetID:    assetID,
		ChunkIndex: chunkIndex,
		KeyID:      keyID,
		Nonce:      nonce,
		Ciphertext: sealed,
	}, nil
}

// Decrypt opens an envelope. Structural problems are reported as truncation
// or invalid input; any GCM failure (tampered bytes, swapped metadata, wrong
// key, truncated tag) is reported as auth_failed. No plaintext is returned on
// failure.
func Decrypt(key []byte, env *manifest.Envelope) ([]byte, error) {
	if env == nil {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, "nil envelope")
	}
	if env.Version != manifest.FormatVersion {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, "unsupported envelope version")
	}
	if env.AssetID == "" || env.KeyID == "" || env.ChunkIndex < 0 {
		return nil, manifest.NewError(manifest.ReasonInvalidInput, "missing envelope metadata")
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(env.Nonce) != gcm.NonceSize() {
		return nil, manifest.NewError(manifest.ReasonTruncated, "nonce length mismatch")
	}
	if len(env.Ciphertext) < gcm.Overhead() {
		return nil, manifest.NewError(manifest.ReasonTruncated, "ciphertext shorter than GCM tag")
	}
	aad := DeriveAAD(env.AssetID, env.ChunkIndex, env.KeyID)
	plaintext, err := gcm.Open(nil, env.Nonce, env.Ciphertext, aad)
	if err != nil {
		return nil, manifest.NewError(manifest.ReasonAuthFailed, "GCM authentication failed")
	}
	return plaintext, nil
}
