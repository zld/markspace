// Package metrics computes the content hashes and quantitative summaries
// the acceptance report publishes.
//
// Frame hashing is over the coded sample payloads, in decode order, so it
// detects a different cut at the container level even when the video looks
// identical. Pixel mean is computed over raw decoded-looking payload bytes
// (the fixture carries a deterministic byte pattern); it is a structural
// statistic, not a colour model.
package metrics

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

// SampleHasher accumulates a frame hash over coded samples.
type SampleHasher struct {
	h     hash.Hash
	count int
	bytes int64
}

// NewSampleHasher returns an empty hasher.
func NewSampleHasher() *SampleHasher { return &SampleHasher{h: sha256.New()} }

// AddSample feeds one coded sample. The 4-byte big-endian length prefix
// keeps concatenation unambiguous.
func (s *SampleHasher) AddSample(b []byte) {
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(b)))
	s.h.Write(lenBuf[:])
	s.h.Write(b)
	s.count++
	s.bytes += int64(len(b))
}

// Sum returns the hex digest.
func (s *SampleHasher) Sum() string { return hex.EncodeToString(s.h.Sum(nil)) }

// Count returns the numbers of samples hashed.
func (s *SampleHasher) Count() int { return s.count }

// Bytes returns the total payload bytes hashed.
func (s *SampleHasher) Bytes() int64 { return s.bytes }

// HashSamples hashes a slice of separate coded samples.
func HashSamples(samples [][]byte) string {
	h := NewSampleHasher()
	for _, b := range samples {
		h.AddSample(b)
	}
	return h.Sum()
}

// HashReader hashes a whole stream with SHA-256.
func HashReader(r io.Reader) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(h, r)
	if err != nil {
		return "", n, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// HashBytes hashes a byte slice.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// PixelMean returns the arithmetic mean of payload bytes normalised to
// 0..1, treating the payload as a packed pixel-like buffer. It reads in
// bounded blocks and never allocates per byte.
func PixelMean(r io.ReaderAt, offset, size int64) (float64, error) {
	if size <= 0 {
		return 0, nil
	}
	const block = 64 << 10
	buf := make([]byte, block)
	var sum uint64
	var n uint64
	for done := int64(0); done < size; {
		want := int64(block)
		if size-done < want {
			want = size - done
		}
		if _, err := r.ReadAt(buf[:want], offset+done); err != nil {
			return 0, err
		}
		for _, b := range buf[:want] {
			sum += uint64(b)
		}
		n += uint64(want)
		done += want
	}
	if n == 0 {
		return 0, nil
	}
	return float64(sum) / (float64(n) * 255.0), nil
}

// MeanBytes is PixelMean over an in-memory buffer.
func MeanBytes(b []byte) float64 {
	if len(b) == 0 {
		return 0
	}
	var sum uint64
	for _, v := range b {
		sum += uint64(v)
	}
	return float64(sum) / (float64(len(b)) * 255.0)
}

// FormatFloat renders a float with six decimals, the stable textual form
// used in manifests and reports.
func FormatFloat(v float64) string { return fmt.Sprintf("%.6f", v) }

// FormatMillis renders a float millisecond value with three decimals.
func FormatMillis(v float64) string { return fmt.Sprintf("%.3f", v) }
