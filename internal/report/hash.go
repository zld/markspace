// Package report computes per-segment SHA-256 hashes for both raw input
// bytes and the normalized, round-tripped output, and renders a stable
// summary. Keeping the two hash classes separate makes any pipeline-
// induced mutation immediately visible.
package report

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
)

// ResumeMismatchError signals that a resumed run did not reproduce the
// fingerprint or hashes of the original run.
type ResumeMismatchError string

func (e ResumeMismatchError) Error() string {
	return fmt.Sprintf("report: resume verification mismatch at %s", string(e))
}

// ErrResumeMismatch constructs a typed resume mismatch.
func ErrResumeMismatch(where string) error { return ResumeMismatchError(where) }

// SegmentHash pairs the two hash classes for one segment.
type SegmentHash struct {
	Segment     string `json:"segment"`
	MediaSeq    uint64 `json:"media_seq"`
	InputHash   string `json:"input_sha256"`
	OutputHash  string `json:"output_sha256"`
	InputBytes  int64  `json:"input_bytes"`
	OutputBytes int64  `json:"output_bytes"`
	Damaged     bool   `json:"damaged"`
}

// Hasher accumulates per-segment hashes in a deterministic order.
type Hasher struct {
	items []SegmentHash
}

// NewHasher returns an empty hasher.
func NewHasher() *Hasher { return &Hasher{} }

// Sum256 returns the lowercase hex SHA-256 of data.
func Sum256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Add records one segment's input and processed hashes.
func (h *Hasher) Add(s SegmentHash) {
	h.items = append(h.items, s)
}

// Segments returns hashes sorted by media sequence for stable output.
func (h *Hasher) Segments() []SegmentHash {
	out := make([]SegmentHash, len(h.items))
	copy(out, h.items)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].MediaSeq < out[j].MediaSeq
	})
	return out
}

// Len reports how many segments were hashed.
func (h *Hasher) Len() int { return len(h.items) }

// Mismatches returns segments whose output hash differs from the input
// hash while both were fully processed (an expected signal only when the
// transform is supposed to be byte-stable).
func (h *Hasher) Mismatches() []SegmentHash {
	var out []SegmentHash
	for _, s := range h.items {
		if s.Damaged {
			continue
		}
		if s.InputHash != s.OutputHash {
			out = append(out, s)
		}
	}
	return out
}

// Fingerprint folds every input hash into one digest of the ordered
// segment list, giving the whole run a single reproducible identity.
func (h *Hasher) Fingerprint() string {
	ordered := h.Segments()
	acc := sha256.New()
	var seqBytes [8]byte
	for _, s := range ordered {
		binary.BigEndian.PutUint64(seqBytes[:], s.MediaSeq)
		acc.Write(seqBytes[:])
		acc.Write([]byte(s.Segment))
		acc.Write([]byte(s.InputHash))
	}
	return hex.EncodeToString(acc.Sum(nil))
}

// Metrics holds the quantitative summary printed at the end of each run.
type Metrics struct {
	TotalSegments        int     `json:"total_segments"`
	DamagedSegments      int     `json:"damaged_segments"`
	RoundtripConsistency float64 `json:"roundtrip_consistency"`
	KeyframeHitRate      float64 `json:"keyframe_hit_rate"`
	ProcessableAfter     int     `json:"processable_after_damage"`
	Fingerprint          string  `json:"fingerprint"`
	ExitCode             int     `json:"exit_code"`
}

// Render serializes the summary deterministically (indented JSON).
func (m Metrics) Render() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}
