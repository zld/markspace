// Package digest computes the two-tier content identity of a segment.
// The raw hash covers transport bytes exactly as stored; the content hash
// covers only demultiplexed payload bytes, so a remux that preserves payload
// changes the raw hash but not the content hash, letting the planner tell
// source-content changes apart from encapsulation-only changes.
package digest

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
)

// Size is the number of bytes in a SHA-256 digest.
const Size = sha256.Size

// Digests pairs the transport-level and content-level hashes of a segment.
type Digests struct {
	// Raw is SHA-256 over every transport byte in the segment (188*N).
	Raw [Size]byte
	// Content is SHA-256 over the concatenated demuxed payloads, excluding
	// headers, adaptation fields, stuffing and PSI tables.
	Content [Size]byte
}

// Hex returns the lowercase hex forms used in sidecars and reports.
func (d Digests) Hex() (raw, content string) {
	return hex.EncodeToString(d.Raw[:]), hex.EncodeToString(d.Content[:])
}

// Equal reports whether both hashes match.
func (d Digests) Equal(o Digests) bool {
	return d.Raw == o.Raw && d.Content == o.Content
}

// ContentEqual reports whether demuxed content is identical even if the
// encapsulation (and therefore the raw hash) differs.
func (d Digests) ContentEqual(o Digests) bool {
	return d.Content == o.Content
}

// ChangeKind classifies how a freshly computed digest relates to a previously
// indexed one. This is what lets the planner treat a remux differently from a
// real source-content edit.
type ChangeKind uint8

const (
	// ChangeUnchanged: neither transport bytes nor demuxed content moved.
	ChangeUnchanged ChangeKind = iota
	// ChangeEncapsulationOnly: content hashes match but the raw transport
	// bytes differ (e.g. a remux, restuffing or CC/PCR rewrite).
	ChangeEncapsulationOnly
	// ChangeContent: demuxed content changed, so the asset and anything that
	// depends on its keyframes must be recomputed.
	ChangeContent
)

// String returns a stable lowercase name used in logs and sidecars.
func (k ChangeKind) String() string {
	switch k {
	case ChangeEncapsulationOnly:
		return "encapsulation-only"
	case ChangeContent:
		return "content"
	default:
		return "unchanged"
	}
}

// Classify compares a fresh digest against the stored one.
func (d Digests) Classify(stored Digests) ChangeKind {
	if d.Equal(stored) {
		return ChangeUnchanged
	}
	if d.ContentEqual(stored) {
		return ChangeEncapsulationOnly
	}
	return ChangeContent
}

// Hasher incrementally builds both hashes while a segment streams through.
// It never retains the packet bodies, only the rolling hash state.
type Hasher struct {
	raw     hash.Hash
	content hash.Hash
	packets int
	bytes   int64
}

// NewHasher returns an empty two-tier hasher.
func NewHasher() *Hasher {
	return &Hasher{raw: sha256.New(), content: sha256.New()}
}

// AddPacket feeds one raw 188-byte packet and its already-extracted payload.
// Callers pass a nil payload for table-only or adaptation-only packets so the
// content hash stays focused on elementary-stream bytes.
func (h *Hasher) AddPacket(raw188, payload []byte) {
	h.raw.Write(raw188)
	h.packets++
	h.bytes += int64(len(raw188))
	if len(payload) > 0 {
		h.content.Write(payload)
	}
}

// Packets returns the number of packets hashed so far.
func (h *Hasher) Packets() int { return h.packets }

// RawBytes returns the number of transport bytes hashed so far.
func (h *Hasher) RawBytes() int64 { return h.bytes }

// Sum finalizes both hashes. The hasher may continue to be used afterwards;
// SHA-256 Sum does not reset the underlying hash state.
func (h *Hasher) Sum() Digests {
	var d Digests
	h.raw.Sum(d.Raw[:0])
	h.content.Sum(d.Content[:0])
	return d
}
