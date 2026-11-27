// Package merge combines per-task segment lists into one deterministic
// delivery manifest keyed by stable task keys.
package merge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"mediad/internal/clock"
)

// Segment is one delivered interval: closed-open [StartNs, EndNs), the
// source byte extent it was read from, and the SHA-256 of those bytes.
type Segment struct {
	TaskKey   string `json:"task_key"`
	StartNs   int64  `json:"start_ns"`
	EndNs     int64  `json:"end_ns"`
	SrcOffset int64  `json:"src_offset"`
	SrcLength int64  `json:"src_length"`
	SHA256    string `json:"sha256"`
}

// equal reports content equality (everything but the task key, which the
// caller already matched).
func (s Segment) equal(o Segment) bool {
	return s.StartNs == o.StartNs && s.EndNs == o.EndNs &&
		s.SrcOffset == o.SrcOffset && s.SrcLength == o.SrcLength &&
		s.SHA256 == o.SHA256
}

// Manifest is the deterministic delivery manifest.
type Manifest struct {
	Segments []Segment `json:"segments"`
}

// Merge combines segment lists from multiple tasks. Inputs are sorted by
// stable task key; a duplicate key with identical content is merged once,
// a duplicate key with conflicting content is an error.
func Merge(lists ...[]Segment) (*Manifest, error) {
	byKey := make(map[string]Segment)
	var keys []string
	for _, list := range lists {
		for _, seg := range list {
			if seg.TaskKey == "" {
				return nil, clock.New(clock.ErrDuplicateKey, "segment without task key")
			}
			prev, seen := byKey[seg.TaskKey]
			if !seen {
				byKey[seg.TaskKey] = seg
				keys = append(keys, seg.TaskKey)
				continue
			}
			if !prev.equal(seg) {
				return nil, clock.New(clock.ErrKeyConflict,
					"task key %q has conflicting content", seg.TaskKey)
			}
			// Identical duplicate: merged once, nothing to do.
		}
	}
	sort.Strings(keys)
	m := &Manifest{Segments: make([]Segment, 0, len(keys))}
	for _, k := range keys {
		m.Segments = append(m.Segments, byKey[k])
	}
	return m, nil
}

// Bytes renders the manifest as canonical, stable JSON.
func (m *Manifest) Bytes() ([]byte, error) {
	return json.MarshalIndent(m, "", "  ")
}

// Hash is the SHA-256 of the canonical manifest bytes.
func (m *Manifest) Hash() (string, error) {
	b, err := m.Bytes()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// HashBytes computes the SHA-256 hex of raw source bytes; callers use it to
// fill Segment.SHA256 from the exact source extent.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
