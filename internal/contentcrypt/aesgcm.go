// Package contentcrypt wraps AES-256-GCM encryption with authenticated
// associated data binding ciphertext to its segment metadata.
package contentcrypt

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"

	"mediaseal/internal/media"
)

// NonceSize is the GCM nonce size used for every sealed segment.
const NonceSize = 12

// AAD binds the asset identifier, track, segment number, PTS and key ID
// into the authentication tag; tampering with any of them, or with the
// ciphertext, fails decryption.
func AAD(seg media.Segment, keyID string) []byte {
	return []byte(fmt.Sprintf("%s|%s|%d|%d|%s",
		seg.AssetID, seg.Track, seg.Index, seg.PTS, keyID))
}

// Encrypt seals plaintext under key with a random nonce, authenticating aad.
func Encrypt(key, plaintext, aad []byte) (nonce, ciphertext []byte, err error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, nil, err
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, nil, fmt.Errorf("generate gcm nonce: %w", err)
	}
	return nonce, gcm.Seal(nil, nonce, plaintext, aad), nil
}

// Decrypt opens ciphertext under key, authenticating aad.
func Decrypt(key, nonce, ciphertext, aad []byte) ([]byte, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	plain, err := gcm.Open(nil, nonce, ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("gcm open: %w", err)
	}
	return plain, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("data key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("init aes: %w", err)
	}
	return cipher.NewGCM(block)
}
