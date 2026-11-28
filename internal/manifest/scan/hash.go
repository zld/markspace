// Package scan builds a content-addressed index for a parsed manifest. Every
// file that participates in processing is hashed with SHA-256, and the index
// is only committed after the whole input set has been verified.
package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"io"
	"os"

	"archive-lab/internal/media"
)

// Hasher wraps SHA-256 with the byte accounting the index needs.
type Hasher struct {
	h hash.Hash
	n int64
}

// NewHasher returns a fresh SHA-256 hasher.
func NewHasher() *Hasher { return &Hasher{h: sha256.New()} }

// Write feeds bytes through the hasher.
func (h *Hasher) Write(p []byte) (int, error) {
	n, err := h.h.Write(p)
	h.n += int64(n)
	return n, err
}

// Bytes hashed so far.
func (h *Hasher) Bytes() int64 { return h.n }

// SumHex returns the lowercase hex digest.
func (h *Hasher) SumHex() string { return hex.EncodeToString(h.h.Sum(nil)) }

// HashBytes hashes an in-memory buffer.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// FileDigest is the result of hashing one file.
type FileDigest struct {
	Path   string
	SHA256 string
	Bytes  int64
}

// HashFile streams a file through SHA-256. maxBytes limits how much is read;
// a larger file is reported as an error rather than partially hashed.
func HashFile(path string, maxBytes int64) (FileDigest, error) {
	f, err := os.Open(path)
	if err != nil {
		return FileDigest{}, media.Wrap(media.StageHash, "hash", err, "cannot open file").WithPath(path)
	}
	defer f.Close()
	h := NewHasher()
	buf := make([]byte, 256*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			if maxBytes > 0 && h.Bytes() > maxBytes {
				return FileDigest{}, media.New(media.StageHash, "hash", "file exceeds %d byte limit", maxBytes).WithPath(path)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return FileDigest{}, media.Wrap(media.StageHash, "hash", rerr, "read failed").WithPath(path)
		}
	}
	return FileDigest{Path: path, SHA256: h.SumHex(), Bytes: h.Bytes()}, nil
}
