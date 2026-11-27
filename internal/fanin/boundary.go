// Package fanin cross-checks independently recovered representations against
// a shared clock. Segment start/end boundaries must agree across bitrates
// within a configured tolerance; mismatches are reported rather than merged.
package fanin

import (
	"tsrepair/internal/mediaerr"
	"tsrepair/internal/segment"
)

// DefaultBoundaryToleranceNanos is the 1 millisecond requirement: boundary
// differences at or below this are considered consistent.
const DefaultBoundaryToleranceNanos int64 = 1_000_000

// BoundaryMismatch describes one cross-representation disagreement.
type BoundaryMismatch struct {
	SegmentID       int   `json:"segment_id"`
	RepresentationA int   `json:"representation_a"`
	RepresentationB int   `json:"representation_b"`
	Edge            string `json:"edge"` // "start" or "end"
	TimeA           int64 `json:"time_a_nanos"`
	TimeB           int64 `json:"time_b_nanos"`
	DeltaNanos      int64 `json:"delta_nanos"`
}

// Result is the fan-in verdict for one segment position.
type Result struct {
	SegmentID  int
	Consistent bool
	Mismatches []BoundaryMismatch
	// Reference bounds chosen from the lowest successful representation id,
	// used only when the segment is fully consistent.
	StartNanos int64
	EndNanos   int64
}

// Checker holds the shared-clock tolerance and accumulates mismatches.
type Checker struct {
	ToleranceNanos int64
	ec             mediaerr.Collector
}

// NewChecker builds a checker with the given tolerance (use
// DefaultBoundaryToleranceNanos for the standard 1 ms rule).
func NewChecker(toleranceNanos int64) *Checker {
	if toleranceNanos <= 0 {
		toleranceNanos = DefaultBoundaryToleranceNanos
	}
	return &Checker{ToleranceNanos: toleranceNanos}
}

// Errors returns structured boundary errors recorded during checking.
func (c *Checker) Errors() []*mediaerr.Error { return c.ec.All() }

// MismatchCount returns the total number of boundary disagreements.
func (c *Checker) MismatchCount() int { return c.ec.CountByCode(mediaerr.CodeBoundaryMismatch) }

// CheckSegment compares the segments (one per successful representation) that
// occupy the same segment position. Segments are taken from a map keyed by
// representation id. A single representation is trivially consistent.
func (c *Checker) CheckSegment(segmentID int, perRep map[int]*segment.Segment) Result {
	ids := sortedKeys(perRep)
	res := Result{SegmentID: segmentID, Consistent: true}
	if len(ids) == 0 {
		res.Consistent = false
		return res
	}
	ref := perRep[ids[0]]
	res.StartNanos = ref.StartNanos
	res.EndNanos = ref.EndNanos

	for _, id := range ids[1:] {
		s := perRep[id]
		if d := abs(s.StartNanos - ref.StartNanos); d > c.ToleranceNanos {
			res.Consistent = false
			m := BoundaryMismatch{
				SegmentID: segmentID, RepresentationA: ids[0], RepresentationB: id,
				Edge: "start", TimeA: ref.StartNanos, TimeB: s.StartNanos, DeltaNanos: d,
			}
			res.Mismatches = append(res.Mismatches, m)
			c.record(m)
		}
		if d := abs(s.EndNanos - ref.EndNanos); d > c.ToleranceNanos {
			res.Consistent = false
			m := BoundaryMismatch{
				SegmentID: segmentID, RepresentationA: ids[0], RepresentationB: id,
				Edge: "end", TimeA: ref.EndNanos, TimeB: s.EndNanos, DeltaNanos: d,
			}
			res.Mismatches = append(res.Mismatches, m)
			c.record(m)
		}
	}
	return res
}

func (c *Checker) record(m BoundaryMismatch) {
	c.ec.Add(mediaerr.Recoverable(mediaerr.CodeBoundaryMismatch, 0, 0,
		"segment boundary differs beyond tolerance",
		"flag segment inconsistent; do not merge failing representations"))
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// sortedKeys returns map keys in ascending order without relying on map
// iteration, keeping fan-in output deterministic.
func sortedKeys(m map[int]*segment.Segment) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// CheckAll groups every representation's segments by segment id and checks
// each position. Only positions present in all successful representations
// are checked; a missing counterpart is itself a mismatch.
func (c *Checker) CheckAll(perRepSegments map[int][]*segment.Segment) []Result {
	positions := map[int]map[int]*segment.Segment{}
	var order []int
	seen := map[int]bool{}
	for repID, segs := range perRepSegments {
		for _, s := range segs {
			if positions[s.ID] == nil {
				positions[s.ID] = map[int]*segment.Segment{}
			}
			positions[s.ID][repID] = s
			if !seen[s.ID] {
				seen[s.ID] = true
				order = append(order, s.ID)
			}
		}
	}
	sortInts(order)
	var out []Result
	for _, id := range order {
		out = append(out, c.CheckSegment(id, positions[id]))
	}
	return out
}

func sortInts(v []int) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j-1] > v[j]; j-- {
			v[j-1], v[j] = v[j], v[j-1]
		}
	}
}
