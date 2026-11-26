package media

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
)

// DigestKind separates material content from encapsulation structure so that
// two assets carrying identical payload inside different containers are not
// treated as duplicates, while the same payload always is.
type DigestKind string

const (
	// KindPayload hashes the material body (cue text, sample/frame content,
	// RTP payloads). Path names and modification times are never included.
	KindPayload DigestKind = "payload"
	// KindContainer hashes encapsulation structure (box layout, RTP framing,
	// header ordering) independently of the material body.
	KindContainer DigestKind = "container"
)

// Digester is a streaming SHA-256 hasher bound to a digest kind.
type Digester struct {
	kind DigestKind
	h    hash.Hash
}

// NewDigester opens a content digest. The kind prefix is written first so the
// same bytes under different kinds never collide.
func NewDigester(kind DigestKind) *Digester {
	d := &Digester{kind: kind, h: sha256.New()}
	_, _ = d.h.Write([]byte(kind))
	_, _ = d.h.Write([]byte{0})
	return d
}

// Write feeds bytes into the digest.
func (d *Digester) Write(p []byte) (int, error) { return d.h.Write(p) }

// WriteString feeds a string into the digest.
func (d *Digester) WriteString(s string) { _, _ = d.h.Write([]byte(s)) }

// Sum returns the hex-encoded digest, prefixed by its kind, e.g.
// "payload:1f2a...".
func (d *Digester) Sum() string {
	return string(d.kind) + ":" + hex.EncodeToString(d.h.Sum(nil))
}

// DigestBytes is the one-shot form for data already in memory.
func DigestBytes(kind DigestKind, data []byte) string {
	d := NewDigester(kind)
	_, _ = d.Write(data)
	return d.Sum()
}

// DigestReader streams an entire reader through a digest.
func DigestReader(kind DigestKind, r io.Reader) (string, error) {
	d := NewDigester(kind)
	if _, err := io.Copy(d.h, r); err != nil {
		return "", err
	}
	return d.Sum(), nil
}
