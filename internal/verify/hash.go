// Package verify provides streaming digests used to prove that remuxing
// preserved sample bytes and that re-runs are byte-identical.
package verify

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

// Hasher wraps a SHA-256 stream with a byte counter.
type Hasher struct {
	h hash.Hash
	n int64
}

// NewHasher returns a fresh hasher.
func NewHasher() *Hasher { return &Hasher{h: sha256.New()} }

// Write implements io.Writer.
func (h *Hasher) Write(p []byte) (int, error) {
	h.n += int64(len(p))
	return h.h.Write(p)
}

// Bytes returns bytes written so far.
func (h *Hasher) Bytes() int64 { return h.n }

// Sum returns the hex digest.
func (h *Hasher) Sum() string { return hex.EncodeToString(h.h.Sum(nil)) }

// HashBytes hashes a byte slice and returns the hex digest.
func HashBytes(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// HashFile streams a file through SHA-256 with a bounded buffer.
func HashFile(path string, r io.Reader, limit int64) (string, int64, error) {
	h := NewHasher()
	var n int64
	buf := make([]byte, 64*1024)
	for {
		if limit > 0 && n >= limit {
			break
		}
		want := int64(len(buf))
		if limit > 0 && limit-n < want {
			want = limit - n
		}
		read, err := r.Read(buf[:want])
		if read > 0 {
			h.Write(buf[:read])
			n += int64(read)
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", n, fmt.Errorf("hash %s after %d bytes: %w", path, n, err)
		}
	}
	return h.Sum(), n, nil
}

// Compare asserts two digests are equal, returning a descriptive error that
// includes both so a mismatch is actionable.
func Compare(what, want, got string) error {
	if want == got {
		return nil
	}
	return fmt.Errorf("%s mismatch: want %s got %s", what, want, got)
}

// RangeHasher hashes sample byte ranges within a seekable reader, in the order
// given, without loading the whole file.
type RangeHasher struct {
	r   io.ReadSeeker
	buf []byte
}

// NewRangeHasher builds a hasher over r.
func NewRangeHasher(r io.ReadSeeker) *RangeHasher {
	return &RangeHasher{r: r, buf: make([]byte, 64*1024)}
}

// HashRanges hashes the concatenation of [offset, offset+length) ranges.
func (rh *RangeHasher) HashRanges(ranges []Range) (string, int64, error) {
	h := NewHasher()
	var total int64
	for _, rg := range ranges {
		if rg.Length < 0 || rg.Offset < 0 {
			return "", total, fmt.Errorf("invalid range offset=%d length=%d", rg.Offset, rg.Length)
		}
		if _, err := rh.r.Seek(rg.Offset, io.SeekStart); err != nil {
			return "", total, err
		}
		left := rg.Length
		for left > 0 {
			want := int64(len(rh.buf))
			if left < want {
				want = left
			}
			n, err := io.ReadFull(rh.r, rh.buf[:want])
			if n > 0 {
				h.Write(rh.buf[:n])
				total += int64(n)
				left -= int64(n)
			}
			if err != nil {
				return "", total, fmt.Errorf("range offset=%d: %w", rg.Offset, err)
			}
		}
	}
	return h.Sum(), total, nil
}

// Range is a byte range in a file.
type Range struct {
	Offset int64
	Length int64
}
