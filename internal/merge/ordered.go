// Package merge provides a deterministic, deduplicating ordered merge over
// indexed records, plus the manifest row type shared by the pipeline.
package merge

import (
	"sort"
)

// Record is one manifest row. Identity is (Asset, Seq, Stage); ordering is by
// asset, then exact start time, then original sequence number, so merging is
// stable regardless of worker scheduling.
type Record struct {
	Asset   string
	StartMs float64
	Seq     int64
	Stage   string
	Kind    string
	Payload string
}

// Key identifies a record for deduplication.
type Key struct {
	Asset string
	Seq   int64
	Stage string
	Kind  string
}

// Key returns the deduplication key.
func (r Record) Key() Key { return Key{Asset: r.Asset, Seq: r.Seq, Stage: r.Stage, Kind: r.Kind} }

// Less is the total order used by Merge: asset, start time, sequence number,
// stage and kind. It is deliberately total so output never depends on sort
// stability or arrival order.
func Less(a, b Record) bool {
	if a.Asset != b.Asset {
		return a.Asset < b.Asset
	}
	if a.StartMs != b.StartMs {
		return a.StartMs < b.StartMs
	}
	if a.Seq != b.Seq {
		return a.Seq < b.Seq
	}
	if a.Stage != b.Stage {
		return a.Stage < b.Stage
	}
	if a.Kind != b.Kind {
		return a.Kind < b.Kind
	}
	return a.Payload < b.Payload
}

// MergeResult reports what the merge stage did, for audit.
type MergeResult struct {
	Rows       []Record
	Duplicates int64
	Input      int64
}

// Merge sorts records deterministically and drops exact-identity duplicates.
// When two records share a key but differ in payload, the first in total order
// wins and the record is counted as a conflict duplicate; payload selection is
// still deterministic because the input order was normalised first.
func Merge(records []Record) MergeResult {
	in := append([]Record(nil), records...)
	sort.Slice(in, func(i, j int) bool { return Less(in[i], in[j]) })
	out := make([]Record, 0, len(in))
	seen := make(map[Key]struct{}, len(in))
	res := MergeResult{Input: int64(len(in))}
	for _, r := range in {
		k := r.Key()
		if _, ok := seen[k]; ok {
			res.Duplicates++
			continue
		}
		seen[k] = struct{}{}
		out = append(out, r)
	}
	res.Rows = out
	return res
}

// MergeStreams interleaves several already-sorted record streams into one
// deterministically ordered stream. Each stream must be sorted by Less.
func MergeStreams(streams [][]Record) []Record {
	var total int
	for _, s := range streams {
		total += len(s)
	}
	out := make([]Record, 0, total)
	idx := make([]int, len(streams))
	for {
		best := -1
		for i, s := range streams {
			if idx[i] >= len(s) {
				continue
			}
			if best < 0 || Less(s[idx[i]], streams[best][idx[best]]) {
				best = i
			}
		}
		if best < 0 {
			break
		}
		out = append(out, streams[best][idx[best]])
		idx[best]++
	}
	return out
}

// ValidateMonotone checks that rows are strictly ordered by total order.
func ValidateMonotone(rows []Record) (violations int, firstAt int) {
	for i := 1; i < len(rows); i++ {
		if Less(rows[i], rows[i-1]) {
			return 1, i
		}
	}
	return 0, -1
}

// Manifest ordering invariant checks are shared with the pipeline.
type Manifest struct {
	Asset    string
	Rows     []Record
	Segments []SegmentRef
	Digest   string
}

// SegmentRef is the merge package's view of a damage segment, avoiding an
// import cycle with internal/stream.
type SegmentRef struct {
	Source      string
	Code        string
	StartMs     float64
	EndMs       float64
	StartOffset int64
	EndOffset   int64
}
