package segment

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"tracecheck/internal/clock"
	"tracecheck/internal/probe"
)

// Segment describes one aligned shard across all representations.
type Segment struct {
	Index        int                       `json:"index"`
	Start        int64                     `json:"start_tick"`
	End          int64                     `json:"end_tick"`
	MemberStarts map[uint32]int64          `json:"member_starts"`
	Members      map[uint32]MemberRange    `json:"members"`
}

// MemberRange pins one representation's samples for the shard.
type MemberRange struct {
	FirstSample uint32 `json:"first_sample"`
	LastSample  uint32 `json:"last_sample"`
	StartOffset int64  `json:"start_offset"`
	EndOffset   int64  `json:"end_offset"`
}

// Manifest is the deterministic output of the segmentation stage.
type Manifest struct {
	SchemaVersion int                 `json:"schema_version"`
	InputDigest   string              `json:"input_digest"`
	TrackIDs      []uint32            `json:"track_ids"`
	Segments      []Segment           `json:"segments"`
	MaxDelta      int64               `json:"max_boundary_delta"`
	SegmentDigest string              `json:"segment_digest"`
}

// Build constructs segments from an alignment and the recovered samples.
func Build(al *Alignment, recovered map[uint32][]probe.RecoveredSample, inputDigest string) (*Manifest, error) {
	ids := sortedTrackIDs(recovered)
	bounds := al.Boundaries
	segments := make([]Segment, 0, len(bounds))

	for gi, bnd := range bounds {
		end := clock.Tick(0)
		if gi+1 < len(bounds) {
			end = bounds[gi+1].Start
		} else {
			end = tailEnd(recovered)
		}
		members := make(map[uint32]MemberRange, len(ids))
		memberStarts := make(map[uint32]int64, len(ids))
		for _, id := range ids {
			mr, ok := memberRange(recovered[id], bnd.Start, end)
			if !ok {
				return nil, clock.NewOffsetError(clock.Offset(bnd.MemberStarts[id]),
					clock.CategoryMissingIndex,
					fmt.Sprintf("group %d missing representation %d", gi, id), nil)
			}
			members[id] = mr
			memberStarts[id] = int64(bnd.MemberStarts[id])
		}
		segments = append(segments, Segment{
			Index: gi, Start: int64(bnd.Start), End: int64(end),
			MemberStarts: memberStarts, Members: members,
		})
	}

	m := &Manifest{
		SchemaVersion: 1,
		InputDigest:   inputDigest,
		TrackIDs:      ids,
		Segments:      segments,
		MaxDelta:      maxBoundary(al),
	}
	digest, err := m.computeDigest()
	if err != nil {
		return nil, err
	}
	m.SegmentDigest = digest
	return m, nil
}

// memberRange collects samples with start in [groupStart, groupEnd).
func memberRange(samples []probe.RecoveredSample, groupStart, groupEnd clock.Tick) (MemberRange, bool) {
	var first, last *probe.RecoveredSample
	for i := range samples {
		s := &samples[i]
		if s.Start < groupStart || s.Start >= groupEnd {
			continue
		}
		if first == nil {
			first = s
		}
		last = s
	}
	if first == nil {
		return MemberRange{}, false
	}
	return MemberRange{
		FirstSample: first.Index,
		LastSample:  last.Index,
		StartOffset: int64(first.Offset),
		EndOffset:   int64(last.Offset) + last.Size,
	}, true
}

func tailEnd(recovered map[uint32][]probe.RecoveredSample) clock.Tick {
	var end clock.Tick
	for _, samples := range recovered {
		if n := len(samples); n > 0 {
			if t := samples[n-1].Start + samples[n-1].Duration; t > end {
				end = t
			}
		}
	}
	return end
}

func maxBoundary(al *Alignment) int64 {
	var m int64
	for _, b := range al.Boundaries {
		if int64(b.MaxDelta) > m {
			m = int64(b.MaxDelta)
		}
	}
	return m
}

// computeDigest hashes the canonical JSON without the digest field, so the
// digest covers every content field and reruns produce identical hashes.
func (m *Manifest) computeDigest() (string, error) {
	copy := *m
	copy.SegmentDigest = ""
	raw, err := canonicalJSON(&copy)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// Encode renders the manifest as canonical JSON.
func (m *Manifest) Encode() ([]byte, error) {
	raw, err := canonicalJSON(m)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return nil, err
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// canonicalJSON sorts map keys so encoding is independent of Go map iteration.
func canonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var dec any
	if err := json.Unmarshal(raw, &dec); err != nil {
		return nil, err
	}
	return json.Marshal(sortJSON(dec))
}

// sortJSON recursively sorts object keys; slices keep their order.
func sortJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sortStrings(keys)
		out := map[string]any{}
		for _, k := range keys {
			out[k] = sortJSON(t[k])
		}
		return out
	case []any:
		for i := range t {
			t[i] = sortJSON(t[i])
		}
		return t
	default:
		return v
	}
}

func sortStrings(xs []string) {
	for i := 1; i < len(xs); i++ {
		for j := i; j > 0 && xs[j-1] > xs[j]; j-- {
			xs[j-1], xs[j] = xs[j], xs[j-1]
		}
	}
}

// DiffCount compares two manifests field-by-field and returns how many segment
// entries differ. Zero means a byte-stable rerun.
func DiffCount(a, b *Manifest) int {
	if a.InputDigest != b.InputDigest || len(a.Segments) != len(b.Segments) ||
		len(a.TrackIDs) != len(b.TrackIDs) {
		return len(a.Segments) + len(b.Segments)
	}
	diff := 0
	for i := range a.Segments {
		if !segmentsEqual(a.Segments[i], b.Segments[i]) {
			diff++
		}
	}
	return diff
}

func segmentsEqual(a, b Segment) bool {
	if a.Index != b.Index || a.Start != b.Start || a.End != b.End ||
		len(a.Members) != len(b.Members) {
		return false
	}
	for id, am := range a.Members {
		bm, ok := b.Members[id]
		if !ok || am != bm {
			return false
		}
	}
	return true
}
