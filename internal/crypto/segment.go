// Package crypto implements AES-256-GCM segment encryption and decryption
// plus the integrity checks that let validation classify a failure precisely.
//
// A segment is authenticated with the asset id and the segment index bound in
// as additional authenticated data, so a segment cannot be moved to another
// position or asset without failing authentication.
package crypto

import (
	"crypto/cipher"
	"errors"
	"fmt"
	"io"
)

const (
	// NonceSize is the AES-GCM nonce size in bytes.
	NonceSize = 12
	// TagSize is the AES-GCM authentication tag size in bytes.
	TagSize = 16
)

// Errors returned by this package.
var (
	ErrBadNonce    = errors.New("crypto: bad nonce size")
	ErrNilAEAD     = errors.New("crypto: nil AEAD")
	ErrAuth        = errors.New("crypto: authentication failed")
	ErrShortCipher = errors.New("crypto: ciphertext shorter than the GCM tag")
)

func checkNonce(aead cipher.AEAD, nonce []byte) error {
	if aead == nil {
		return ErrNilAEAD
	}
	if len(nonce) != aead.NonceSize() {
		return fmt.Errorf("%w: got %d, want %d", ErrBadNonce, len(nonce), aead.NonceSize())
	}
	return nil
}

// AdditionalData builds the AAD binding a segment to its asset and position.
func AdditionalData(assetID string, index int) []byte {
	return []byte(fmt.Sprintf("mediakey/v1/asset=%s/segment=%d", assetID, index))
}

// Seal encrypts plaintext with AES-GCM. The caller supplies the nonce; the
// packaging pipeline is responsible for generating it and guaranteeing it is
// unique in the manifest. The returned slice holds ciphertext followed by the
// authentication tag.
func Seal(aead cipher.AEAD, nonce, plaintext []byte, assetID string, index int) ([]byte, error) {
	if err := checkNonce(aead, nonce); err != nil {
		return nil, err
	}
	return aead.Seal(nil, nonce, plaintext, AdditionalData(assetID, index)), nil
}

// Open authenticates and decrypts ciphertext. It returns ErrAuth when GCM
// rejects the input, which covers tampering, a wrong key and a wrong nonce,
// and ErrShortCipher for input that cannot hold a tag.
func Open(aead cipher.AEAD, nonce, ciphertext []byte, assetID string, index int) ([]byte, error) {
	if err := checkNonce(aead, nonce); err != nil {
		return nil, err
	}
	if len(ciphertext) < aead.Overhead() {
		return nil, ErrShortCipher
	}
	plain, err := aead.Open(nil, nonce, ciphertext, AdditionalData(assetID, index))
	if err != nil {
		return nil, ErrAuth
	}
	return plain, nil
}

// NewNonce reads a fresh nonce from r. The packaging pipeline checks the
// returned value against every nonce already present in the manifest.
func NewNonce(r io.Reader, size int) ([]byte, error) {
	if size <= 0 {
		return nil, errors.New("crypto: nonce size must be positive")
	}
	if r == nil {
		return nil, errors.New("crypto: nil nonce source")
	}
	buf := make([]byte, size)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, fmt.Errorf("crypto: nonce: %w", err)
	}
	return buf, nil
}
