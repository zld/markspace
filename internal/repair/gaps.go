// Package repair localises damage on the shared media timeline.
//
// Inputs are defect observations from the transport layer (missing RTP
// sequence spans) and from the container layer (truncated WAV data,
// unreadable MP4 samples). Each observation is expressed as a half-open
// tick interval plus a byte offset where one is meaningful. Overlapping
// intervals from the same source are merged; intervals from different
// sources are kept separate so a report can say whether a clip boundary
// was compromised by the network or by the file itself.
package repair

import (
	"sort"

	"replaycheck/internal/fault"
)

// Gap is a damaged half-open tick interval [Start, End).
type Gap struct {
	Source string `json:"source"`
	Start  int64  `json:"start_tick"`
	End    int64  `json:"end_tick"`
	// ByteOffset is the first unreadable byte for container damage, or -1
	// when the damage is not tied to a byte position.
	ByteOffset int64 `json:"byte_offset"`
	// Kind carries the originating fault kind for reporting.
	Kind fault.Kind `json:"kind"`
	// Detail is a short human-readable note.
	Detail string `json:"detail,omitempty"`
}

// Index is an immutable collection of gaps ordered by start tick.
type Index struct {
	Gaps []Gap `json:"gaps"`
}

// New merges gaps that overlap or touch within the same source.
func New(gaps []Gap) *Index {
	sorted := append([]Gap(nil), gaps...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Source != sorted[j].Source {
			return sorted[i].Source < sorted[j].Source
		}
		if sorted[i].Start != sorted[j].Start {
			return sorted[i].Start < sorted[j].Start
		}
		return sorted[i].End < sorted[j].End
	})
	merged := make([]Gap, 0, len(sorted))
	for _, g := range sorted {
		if g.End <= g.Start {
			continue // empty interval carries no information
		}
		if n := len(merged); n > 0 && merged[n-1].Source == g.Source && g.Start <= merged[n-1].End {
			if g.End > merged[n-1].End {
				merged[n-1].End = g.End
			}
			if merged[n-1].ByteOffset < 0 && g.ByteOffset >= 0 {
				merged[n-1].ByteOffset = g.ByteOffset
			}
			if merged[n-1].Detail == "" {
				merged[n-1].Detail = g.Detail
			}
			continue
		}
		merged = append(merged, g)
	}
	return &Index{Gaps: merged}
}

// Overlaps reports whether any gap intersects [start, end).
func (ix *Index) Overlaps(start, end int64) (Gap, bool) {
	for _, g := range ix.Gaps {
		if g.Start < end && start < g.End {
			return g, true
		}
	}
	return Gap{}, false
}

// OverlapsSource reports whether a gap from the named source intersects.
func (ix *Index) OverlapsSource(start, end int64, source string) (Gap, bool) {
	for _, g := range ix.Gaps {
		if g.Source != source {
			continue
		}
		if g.Start < end && start < g.End {
			return g, true
		}
	}
	return Gap{}, false
}

// FirstBadByte returns the smallest non-negative byte offset recorded, or -1.
func (ix *Index) FirstBadByte() int64 {
	best := int64(-1)
	for _, g := range ix.Gaps {
		if g.ByteOffset < 0 {
			continue
		}
		if best < 0 || g.ByteOffset < best {
			best = g.ByteOffset
		}
	}
	return best
}

// TotalTicks sums the merged interval lengths of a source; an empty source
// name sums all sources.
func (ix *Index) TotalTicks(source string) int64 {
	var n int64
	for _, g := range ix.Gaps {
		if source != "" && g.Source != source {
			continue
		}
		n += g.End - g.Start
	}
	return n
}

// Count counts gaps, optionally filtered by source.
func (ix *Index) Count(source string) int {
	n := 0
	for _, g := range ix.Gaps {
		if source == "" || g.Source == source {
			n++
		}
	}
	return n
}
