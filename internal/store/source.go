// Package store provides immutable source summaries and parameter-scoped
// derived cache keys.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"os"

	"github.com/zld/mediacore/internal/trace"
)

// Source is an immutable, digest-addressed source asset.
type Source struct {
	Path   string `json:"path,omitempty"`
	Digest string `json:"source_digest"`
	Size   int64  `json:"size_bytes"`
	data   []byte
}

// DigestBytes returns the stable source digest.
func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// SourceFromBytes creates an immutable source from bytes. The byte slice is
// copied so later caller mutation cannot change the source.
func SourceFromBytes(data []byte, path string) *Source {
	cp := append([]byte(nil), data...)
	return &Source{Path: path, Digest: DigestBytes(cp), Size: int64(len(cp)), data: cp}
}

// OpenSource reads a local file once and retains an immutable copy.
func OpenSource(path string) (*Source, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, trace.E(trace.CodeInvalidArgument, trace.StageSource, "read source %q: %v", path, err)
	}
	return SourceFromBytes(data, path), nil
}

// Bytes returns a defensive copy of source bytes.
func (s *Source) Bytes() []byte {
	if s == nil {
		return nil
	}
	return append([]byte(nil), s.data...)
}
