package medpipe

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"io"
	"os"
	"strconv"
)

// fixedHasher accumulates length-prefixed fields into one SHA-256, giving
// stable content-derived identifiers without importing time or host data.
type fixedHasher struct{ h hash.Hash }

func newFixedHasher() *fixedHasher { return &fixedHasher{h: sha256.New()} }

func (f *fixedHasher) writeString(s string) {
	var l [4]byte
	binary.BigEndian.PutUint32(l[:], uint32(len(s)))
	f.h.Write(l[:])
	f.h.Write([]byte(s))
}

func (f *fixedHasher) writeUint32(v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	f.h.Write(b[:])
}

// hex12 returns the first 12 hex characters of the digest.
func (f *fixedHasher) hex12() string { return hex.EncodeToString(f.h.Sum(nil))[:12] }

// Digest is the SHA-256 content identity of one capture file. It binds an
// index to the exact input bytes it was built from; any change invalidates
// the index and forces a rebuild.
type Digest struct {
	// SHA256 is the lowercase hex SHA-256 of the full file contents.
	SHA256 string `json:"sha256"`
	// Size is the file length in bytes, checked alongside the hash.
	Size int64 `json:"size"`
}

// String renders the digest for logs and manifest lines.
func (d Digest) String() string { return d.SHA256[:12] + "/" + strconv.FormatInt(d.Size, 10) }

// Equal reports whether two digests describe identical content.
func (d Digest) Equal(other Digest) bool {
	return d.Size == other.Size && d.SHA256 == other.SHA256 && d.SHA256 != ""
}

// HashFile streams a file through SHA-256 with a fixed-size buffer so very
// large captures never need to be held in memory.
func HashFile(path string) (Digest, error) {
	f, err := os.Open(path)
	if err != nil {
		return Digest{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.CopyBuffer(h, f, make([]byte, 64*1024))
	if err != nil {
		return Digest{}, err
	}
	return Digest{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}

// HashReader hashes the full content of r and returns its digest.
func HashReader(r io.Reader) (Digest, error) {
	h := sha256.New()
	n, err := io.CopyBuffer(h, r, make([]byte, 64*1024))
	if err != nil {
		return Digest{}, err
	}
	return Digest{SHA256: hex.EncodeToString(h.Sum(nil)), Size: n}, nil
}
