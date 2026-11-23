// Package evidence computes per-segment SHA-256 hashes over actual sample
// payload bytes and compares input/output hashes after remuxing. Hashes are
// the audit trail: identical inputs and policies must always yield identical
// digests, and isolated ranges are never hashed as if recovered.
package evidence

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"

	"github.com/broadcast/mediad/internal/model"
	"github.com/broadcast/mediad/internal/repair"
)

// SegmentHash binds a digest to one adjudicated segment.
type SegmentHash struct {
	TrackID     uint32 `json:"track_id"`
	FirstSample int    `json:"first_sample"`
	LastSample  int    `json:"last_sample"`
	Status      string `json:"status"`
	SHA256      string `json:"sha256"`
	Bytes       int64  `json:"payload_bytes"`
}

// HashSegments reads payload bytes for every non-isolated segment through
// ra and returns one digest per segment in deterministic track/sample order.
func HashSegments(ra io.ReaderAt, g *model.Graph,
	segments map[uint32][]*repair.Segment) ([]SegmentHash, error) {

	var out []SegmentHash
	for _, id := range g.IDs() {
		t, ok := g.Tracks[id]
		if !ok {
			return nil, fmt.Errorf("evidence: unknown track %d", id)
		}
		for _, seg := range segments[id] {
			if seg.Status == repair.StatusIsolated {
				continue // isolated payload is never presented as evidence
			}
			h, n, err := hashSampleRange(ra, t, seg.FirstSample, seg.LastSample)
			if err != nil {
				return nil, fmt.Errorf("track %d segment %d-%d: %w",
					id, seg.FirstSample, seg.LastSample, err)
			}
			out = append(out, SegmentHash{
				TrackID:     id,
				FirstSample: seg.FirstSample,
				LastSample:  seg.LastSample,
				Status:      string(seg.Status),
				SHA256:      hex.EncodeToString(h),
				Bytes:       n,
			})
		}
	}
	return out, nil
}

// hashSampleRange streams the payload bytes of samples [first,last] into one
// SHA-256. Sample offsets are validated against reads; a short read means the
// container points past available data (truncated mdat) and is reported, not
// silently zero-filled.
func hashSampleRange(ra io.ReaderAt, t *model.Track, first, last int) ([]byte, int64, error) {
	if first < 0 || last >= len(t.Samples) || first > last {
		return nil, 0, fmt.Errorf("sample range %d-%d out of bounds (n=%d)",
			first, last, len(t.Samples))
	}
	h := sha256.New()
	var total int64
	buf := make([]byte, 0, 4096)
	for i := first; i <= last; i++ {
		s := t.Samples[i]
		size := int64(s.Size)
		if size > int64(cap(buf)) {
			buf = make([]byte, size)
		} else {
			buf = buf[:size]
		}
		n, err := ra.ReadAt(buf, int64(s.Offset))
		// ReadAt returns EOF only when zero bytes were read at absolute EOF;
		// any short read comes back wrapped and must be treated as truncation.
		if err != nil {
			return nil, total, &TruncatedPayloadError{
				TrackID: t.ID, Sample: i, Offset: s.Offset, Want: size, Got: int64(n),
			}
		}
		if int64(n) != size {
			return nil, total, &TruncatedPayloadError{
				TrackID: t.ID, Sample: i, Offset: s.Offset, Want: size, Got: int64(n),
			}
		}
		if _, err := h.Write(buf); err != nil {
			return nil, total, err
		}
		total += size
	}
	return h.Sum(nil), total, nil
}

// TruncatedPayloadError means a sample offset cannot be fully read.
type TruncatedPayloadError struct {
	TrackID       uint32
	Sample        int
	Offset        uint64
	Want, Got     int64
}

func (e *TruncatedPayloadError) Error() string {
	return fmt.Sprintf("truncated payload: track %d sample %d at offset %d wants %d bytes, got %d",
		e.TrackID, e.Sample, e.Offset, e.Want, e.Got)
}

// Compare returns the fraction (0..1) of input segment digests that match the
// same segment in the output set. Order-independent but identity-sensitive:
// every input segment must exist in the output with an identical digest.
func Compare(in, out []SegmentHash) float64 {
	if len(in) == 0 {
		return 1
	}
	key := func(h SegmentHash) string {
		return fmt.Sprintf("%d:%d:%d", h.TrackID, h.FirstSample, h.LastSample)
	}
	outMap := make(map[string]string, len(out))
	for _, h := range out {
		outMap[key(h)] = h.SHA256
	}
	matched := 0
	for _, h := range in {
		if outMap[key(h)] == h.SHA256 {
			matched++
		}
	}
	return float64(matched) / float64(len(in))
}

// StableDigests reports whether two hash runs are byte-for-byte identical,
// including ordering. This is the determinism gate for repeated runs.
func StableDigests(a, b []SegmentHash) bool {
	if len(a) != len(b) {
		return false
	}
	ca := append([]SegmentHash(nil), a...)
	cb := append([]SegmentHash(nil), b...)
	sortHash(ca)
	sortHash(cb)
	for i := range ca {
		if ca[i] != cb[i] {
			return false
		}
	}
	return true
}

func sortHash(h []SegmentHash) {
	sort.Slice(h, func(i, j int) bool {
		if h[i].TrackID != h[j].TrackID {
			return h[i].TrackID < h[j].TrackID
		}
		return h[i].FirstSample < h[j].FirstSample
	})
}

// DigestSummary renders hashes for the terminal/JSON report deterministically.
func DigestSummary(h []SegmentHash) []map[string]string {
	sortHash(h)
	out := make([]map[string]string, 0, len(h))
	for _, x := range h {
		out = append(out, map[string]string{
			"track_id":     fmt.Sprintf("%d", x.TrackID),
			"sample_range": fmt.Sprintf("%d-%d", x.FirstSample, x.LastSample),
			"status":       x.Status,
			"sha256":       x.SHA256,
		})
	}
	return out
}
