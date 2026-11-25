package mediakernel

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"hash"
)

// DigestAlgorithm is fixed and documented so fixtures and repeat runs always
// produce comparable hashes.
const DigestAlgorithm = "SHA-256"

// Digest is a self-describing content/container hash in JSON output.
type Digest struct {
	Algorithm string `json:"algorithm"`
	Hex       string `json:"hex"`
}

// Equal compares two digests; a zero-value digest never equals another.
func (d Digest) Equal(other Digest) bool {
	return d.Hex != "" && d.Hex == other.Hex && d.Algorithm == other.Algorithm
}

// String renders "algorithm:hex" for logs.
func (d Digest) String() string { return d.Algorithm + ":" + d.Hex }

// Digester wraps the fixed hash algorithm and implements io.Writer so the raw
// container stream can be hashed through an io.TeeReader without buffering.
type Digester struct {
	h hash.Hash
}

// NewDigester starts a fresh SHA-256 digester.
func NewDigester() *Digester { return &Digester{h: sha256.New()} }

// Write feeds raw bytes into the hash.
func (d *Digester) Write(p []byte) (int, error) { return d.h.Write(p) }

// Sum finalizes the digest without consuming the underlying hash state.
func (d *Digester) Sum() Digest {
	return Digest{Algorithm: DigestAlgorithm, Hex: hex.EncodeToString(d.h.Sum(nil))}
}

// DigestBytes hashes a standalone byte slice.
func DigestBytes(b []byte) Digest {
	d := NewDigester()
	_, _ = d.Write(b)
	return d.Sum()
}

// DigestCanonical hashes the deterministic compact-JSON encoding of v. Go's
// encoding/json emits map keys in sorted order, so structs and string-keyed
// maps hash identically regardless of insertion order. This is the documented
// canonical form used for index and manifest digest comparisons.
func DigestCanonical(v any) (Digest, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return Digest{}, NewError(PhaseDigest, -1, "canonical json marshal failed").WithCause(err)
	}
	return DigestBytes(b), nil
}

// ContentHasher builds the source *content* digest from unique packets only.
// Duplicate packets do not change media content, so the pipeline feeds this
// hasher once per unique sequence. RTP/framing header bytes are excluded: the
// same codec payloads in a different encapsulation keep the same content
// digest. Each unique packet contributes a fixed-width prefix plus the payload
// length and payload bytes, making the encoding unambiguous.
type ContentHasher struct {
	d *Digester
}

// NewContentHasher starts a content digest.
func NewContentHasher() *ContentHasher { return &ContentHasher{d: NewDigester()} }

// AddPacket folds one unique packet into the content digest.
func (c *ContentHasher) AddPacket(p *Packet) {
	var head [8]byte
	head[0] = byte(p.Codec)
	binary.BigEndian.PutUint16(head[2:4], p.Seq)
	binary.BigEndian.PutUint32(head[4:8], p.DeclaredSamples)
	_, _ = c.d.Write(head[:])
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(p.Payload)))
	_, _ = c.d.Write(lenBuf[:])
	_, _ = c.d.Write(p.Payload)
}

// Sum returns the content digest.
func (c *ContentHasher) Sum() Digest { return c.d.Sum() }
