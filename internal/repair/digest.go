// Package repair turns raw demuxed blocks into a monotonic, track-aligned
// stream. This file holds the stable digests used for cross-run comparison.
package repair

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
)

// TimestampDigest hashes the repaired timestamps in stream order.
func (r *Result) TimestampDigest() string {
	h := sha256.New()
	var buf [8]byte
	for _, b := range r.Blocks {
		binary.BigEndian.PutUint64(buf[:], uint64(b.TimeNs))
		h.Write(buf[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DecisionDigest hashes the repair decisions in stream order.
func (r *Result) DecisionDigest() string {
	h := sha256.New()
	var buf [8]byte
	for _, d := range r.Decisions {
		binary.BigEndian.PutUint64(buf[:], uint64(d.Track))
		h.Write(buf[:])
		h.Write([]byte(d.Kind))
		binary.BigEndian.PutUint64(buf[:], uint64(d.Sequence))
		h.Write(buf[:])
		binary.BigEndian.PutUint64(buf[:], uint64(d.OutNs))
		h.Write(buf[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SummaryDigest combines the timestamp and decision digests for the final
// summary hash.
func (r *Result) SummaryDigest() string {
	h := sha256.New()
	h.Write([]byte(r.TimestampDigest()))
	h.Write([]byte(r.DecisionDigest()))
	return hex.EncodeToString(h.Sum(nil))
}
