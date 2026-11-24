package index

import (
	"fmt"
	"sort"

	"mediaroute/internal/model"
)

// BoundarySet answers whether an absolute offset lands on a box boundary.
type BoundarySet struct {
	edges []uint64
}

// NewBoundarySet builds a lookup from the half-open ranges recorded in an
// index. Both the start and the end of every range are boundaries.
func NewBoundarySet(ranges []model.ByteRange) *BoundarySet {
	edges := make([]uint64, 0, len(ranges)*2)
	for _, r := range ranges {
		edges = append(edges, r.Start, r.End)
	}
	sort.Slice(edges, func(i, j int) bool { return edges[i] < edges[j] })
	return &BoundarySet{edges: dedupe(edges)}
}

func dedupe(sorted []uint64) []uint64 {
	out := sorted[:0]
	for i, v := range sorted {
		if i == 0 || v != sorted[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// Contains reports whether off is a boundary.
func (b *BoundarySet) Contains(off uint64) bool {
	if b == nil {
		return false
	}
	i := sort.Search(len(b.edges), func(i int) bool { return b.edges[i] >= off })
	return i < len(b.edges) && b.edges[i] == off
}

// Snapped reports whether off coincides with a boundary, and if not returns
// the nearest boundary so callers can describe the mismatch.
func (b *BoundarySet) Snapped(off uint64) (uint64, bool) {
	if b.Contains(off) {
		return off, true
	}
	if b == nil || len(b.edges) == 0 {
		return 0, false
	}
	i := sort.Search(len(b.edges), func(i int) bool { return b.edges[i] >= off })
	if i == len(b.edges) {
		return b.edges[i-1], false
	}
	if i == 0 {
		return b.edges[0], false
	}
	lo, hi := b.edges[i-1], b.edges[i]
	if off-lo <= hi-off {
		return lo, false
	}
	return hi, false
}

// Validation is the result of checking an index against a boundary set.
type Validation struct {
	ValidEntries int
	MaxAlignment model.Micros
	BadOffsets   []uint64
	Issues       []model.Issue
	NonRandom    int
	TrackSpans   map[uint32]model.ByteRange
}

// Validate checks every index entry: its moof and mdat offsets must land on a
// box boundary, its time range must be positive, and per-track entries must
// not overlap. It never trusts the index; it is meant to fail closed.
func Validate(ix *model.Index, manifest *model.Manifest) (*Validation, error) {
	if ix == nil {
		return nil, model.NewTaskError(model.CategoryCorruptIndex, Stage, "index is nil")
	}
	bs := NewBoundarySet(ix.Boundaries)
	v := &Validation{TrackSpans: make(map[uint32]model.ByteRange, 4)}
	prevEnd := make(map[uint32]model.Micros, 4)
	seen := make(map[[3]uint64]struct{}, len(ix.Entries))
	for _, e := range ix.Entries {
		key := [3]uint64{e.MoofOffset, e.MdatOffset, uint64(e.TrackID)}
		if _, dup := seen[key]; dup {
			return nil, model.NewTaskError(model.CategoryCorruptIndex, Stage,
				fmt.Sprintf("duplicate index entry at moof=%d mdat=%d", e.MoofOffset, e.MdatOffset))
		}
		seen[key] = struct{}{}
		if !bs.Contains(e.MoofOffset) {
			v.BadOffsets = append(v.BadOffsets, e.MoofOffset)
			v.Issues = append(v.Issues, model.Issue{
				Category: model.CategoryIndexMismatch,
				Stage:    Stage,
				Offset:   e.MoofOffset,
				TrackID:  e.TrackID,
				Detail:   "moof offset is not on a box boundary",
			})
			continue
		}
		if !bs.Contains(e.MdatOffset) {
			v.BadOffsets = append(v.BadOffsets, e.MdatOffset)
			v.Issues = append(v.Issues, model.Issue{
				Category: model.CategoryIndexMismatch,
				Stage:    Stage,
				Offset:   e.MdatOffset,
				TrackID:  e.TrackID,
				Detail:   "mdat offset is not on a box boundary",
			})
			continue
		}
		if e.EndMicros <= e.StartMicros {
			return nil, model.NewTaskError(model.CategoryCorruptIndex, Stage,
				fmt.Sprintf("entry with non-positive duration: %d..%d", e.StartMicros, e.EndMicros)).
				WithTrack(e.TrackID)
		}
		if last, ok := prevEnd[e.TrackID]; ok && e.StartMicros < last {
			return nil, model.NewTaskError(model.CategoryCorruptIndex, Stage,
				fmt.Sprintf("track %d index overlaps previous entry (%d < %d)", e.TrackID, e.StartMicros, last)).
				WithTrack(e.TrackID)
		}
		prevEnd[e.TrackID] = e.EndMicros
		if !e.RandomAccess {
			v.NonRandom++
		}
		v.ValidEntries++
	}
	if manifest != nil {
		v.MaxAlignment = alignmentError(ix, manifest)
	}
	return v, nil
}

// alignmentError measures the largest difference between the manifest track
// span and the rebuilt index span for any track in either direction.
func alignmentError(ix *model.Index, m *model.Manifest) model.Micros {
	var worst model.Micros
	for _, tm := range m.Tracks {
		first, last, ok := ix.TrackSpan(tm.TrackID)
		if !ok {
			continue
		}
		if d := model.AbsMicros(first, tm.StartMicros); d > worst {
			worst = d
		}
		if d := model.AbsMicros(last, tm.EndMicros); d > worst {
			worst = d
		}
	}
	return worst
}
