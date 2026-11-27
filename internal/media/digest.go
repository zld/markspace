package media

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
)

// Digest is a SHA-256 hex string. It is the only checksum form emitted in
// manifests so output, checkpoint and provenance records compare byte-for-byte.
type Digest string

// NewDigest hashes r to exhaustion in fixed-size chunks. Callers that need to
// keep memory bounded pass a streaming reader, never a whole file buffer.
func NewDigest(r io.Reader) (Digest, error) {
	h := sha256.New()
	if _, err := io.CopyBuffer(h, r, make([]byte, 64<<10)); err != nil {
		return "", err
	}
	return Digest(hex.EncodeToString(h.Sum(nil))), nil
}

// DigestRange hashes a byte range with a bounded buffer.
func DigestRange(r io.ReaderAt, offset, length int64) (Digest, error) {
	if length < 0 || offset < 0 {
		return "", Errorf(ClassOutOfRange, StageProvenance, "", offset, "negative digest range")
	}
	sr := io.NewSectionReader(r, offset, length)
	return NewDigest(sr)
}

// DigestBytes hashes an in-memory value such as a canonical JSON manifest.
func DigestBytes(b []byte) Digest {
	sum := sha256.Sum256(b)
	return Digest(hex.EncodeToString(sum[:]))
}

// Hasher is a convenience wrapper with an explicit finish step.
type Hasher struct{ h hash.Hash }

// NewHasher starts a SHA-256 stream.
func NewHasher() *Hasher { return &Hasher{h: sha256.New()} }

// Write implements io.Writer.
func (h *Hasher) Write(p []byte) (int, error) { return h.h.Write(p) }

// Sum returns the current digest without mutating the stream.
func (h *Hasher) Sum() Digest {
	return Digest(hex.EncodeToString(h.h.Sum(nil)))
}
