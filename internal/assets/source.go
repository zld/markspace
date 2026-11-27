// Package assets models the immutable source material. A source asset is
// read-only: its identity is the SHA-256 of its bytes, and any change to the
// bytes produces a new identity rather than mutating an existing one.
package assets

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
)

// ErrImmutable is returned when an operation would mutate a sealed asset.
var ErrImmutable = errors.New("assets: source asset is immutable")

// Source is a sealed, content-addressed description of input bytes.
type Source struct {
	// ID is the hex SHA-256 of the payload, used as the asset identity.
	ID string `json:"id"`
	// LogicalName labels the stream without affecting its identity.
	LogicalName string `json:"logical_name"`
	Size        int64  `json:"size"`
	// CreatedAt records when the asset was sealed (audit metadata only).
	CreatedAt time.Time `json:"created_at"`
	// payload is retained in memory for the local demo; on-disk flows read
	// through DigestReader instead.
	payload []byte
}

// Digest computes the hex SHA-256 digest of arbitrary bytes.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// DigestReader streams a reader to a hex SHA-256 digest without loading all
// bytes into memory.
func DigestReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("assets: digest failed: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Seal freezes bytes into an immutable source asset.
func Seal(name string, data []byte) *Source {
	return &Source{
		ID:          Digest(data),
		LogicalName: name,
		Size:        int64(len(data)),
		CreatedAt:   time.Now().UTC(),
		payload:     append([]byte(nil), data...),
	}
}

// SealFile hashes and seals a file without keeping its bytes in memory.
func SealFile(name, path string) (*Source, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("assets: open %s: %w", path, err)
	}
	defer f.Close()
	id, err := DigestReader(f)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return &Source{
		ID: id, LogicalName: name, Size: info.Size(),
		CreatedAt: time.Now().UTC(),
	}, nil
}

// Bytes returns a copy of the sealed payload so callers cannot mutate it.
func (s *Source) Bytes() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.payload...)
}

// Verify re-hashes the in-memory payload and confirms it still matches ID.
// A mismatch means storage corruption and must be treated as fatal.
func (s *Source) Verify() error {
	if s == nil {
		return errors.New("assets: nil source")
	}
	if got := Digest(s.payload); got != s.ID {
		return fmt.Errorf("%w: digest %s does not match sealed id %s", ErrImmutable, got, s.ID)
	}
	if int64(len(s.payload)) != s.Size {
		return fmt.Errorf("%w: size drift %d vs %d", ErrImmutable, len(s.payload), s.Size)
	}
	return nil
}

// SetPayload is deliberately absent: callers create a new Source via Seal
// when bytes change, preserving immutability.
