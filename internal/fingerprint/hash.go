// Package fingerprint derives deterministic SHA-256 hashes from segment
// payloads and marks duplicate content without collapsing the source
// records: every segment keeps its own entry and merely references the
// first segment that produced the same hash.
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
)

// Result is the fingerprint of one segment.
type Result struct {
	SegmentID  uint64 `json:"segment_id"`
	SHA256     string `json:"sha256"`
	PayloadLen int    `json:"payload_len"`
	Duplicate  bool   `json:"duplicate"`
	// FirstOfHash is the segment ID that first produced this hash. It equals
	// SegmentID for unique content, otherwise it points at the origin record.
	FirstOfHash uint64 `json:"first_of_hash"`
}

// Hasher is stateless across runs; state lives in the returned table so
// repeated runs over identical input produce identical output.
type Hasher struct{}

// NewHasher constructs the default SHA-256 fingerprinter.
func NewHasher() *Hasher { return &Hasher{} }

// Hash returns the lowercase hex SHA-256 digest of payload.
func (Hasher) Hash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// Table is the full per-segment fingerprint output in input order.
type Table struct {
	Results []Result `json:"results"`
	// Groups maps each digest to every segment carrying it, in segment order.
	Groups map[string][]uint64 `json:"groups"`
}

// Compute fingerprints every segment and records duplicate relationships.
// Segment IDs are taken from the parallel ids slice; payloads and ids must
// align. Output ordering is deterministic for identical inputs.
func (h Hasher) Compute(ids []uint64, payloads [][]byte) *Table {
	t := &Table{Groups: make(map[string][]uint64)}
	first := make(map[string]uint64)
	for i, payload := range payloads {
		id := ids[i]
		digest := h.Hash(payload)
		res := Result{SegmentID: id, SHA256: digest, PayloadLen: len(payload), FirstOfHash: id}
		if origin, seen := first[digest]; seen {
			res.Duplicate = true
			res.FirstOfHash = origin
		} else {
			first[digest] = id
		}
		t.Groups[digest] = append(t.Groups[digest], id)
		t.Results = append(t.Results, res)
	}
	for k := range t.Groups {
		sort.Slice(t.Groups[k], func(a, b int) bool { return t.Groups[k][a] < t.Groups[k][b] })
	}
	return t
}

// DuplicatePairs returns (later, origin) pairs in segment order.
func (t *Table) DuplicatePairs() [][2]uint64 {
	var pairs [][2]uint64
	for _, r := range t.Results {
		if r.Duplicate {
			pairs = append(pairs, [2]uint64{r.SegmentID, r.FirstOfHash})
		}
	}
	return pairs
}
