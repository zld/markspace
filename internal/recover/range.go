package recover

import (
	"sort"

	auditerrors "media-audit/internal/errors"
)

type Range = auditerrors.ByteRange

// Plan describes the minimal byte set that must be recomputed plus dependency
// ranges required to interpret it.
type Plan struct {
	Code         string  `json:"code"`
	FailedAt     int64   `json:"failed_at"`
	Impacted     Range   `json:"impacted"`
	Dependencies []Range `json:"dependencies,omitempty"`
	Recompute    []Range `json:"recompute"`
	Reuse        []Range `json:"reuse,omitempty"`
	Stage        string  `json:"stage"`
}

// MergeRanges returns sorted disjoint ranges. Exact duplicates and overlaps are
// coalesced so recovery never reports the same successful byte twice.
func MergeRanges(ranges []Range) []Range {
	if len(ranges) == 0 {
		return nil
	}
	in := append([]Range(nil), ranges...)
	sort.Slice(in, func(i, j int) bool {
		if in[i].Start != in[j].Start {
			return in[i].Start < in[j].Start
		}
		return in[i].End < in[j].End
	})
	out := []Range{in[0]}
	for _, r := range in[1:] {
		last := &out[len(out)-1]
		if r.Start <= last.End {
			if r.End > last.End {
				last.End = r.End
			}
			continue
		}
		out = append(out, r)
	}
	return out
}

// SubtractRanges returns safe ranges with every byte in removed excluded.
func SubtractRanges(safe, removed []Range) []Range {
	mergedRemoved := MergeRanges(removed)
	var out []Range
	for _, r := range MergeRanges(safe) {
		cursor := r.Start
		for _, rm := range mergedRemoved {
			if rm.End <= cursor || rm.Start >= r.End {
				continue
			}
			if rm.Start > cursor {
				out = append(out, Range{Start: cursor, End: min64(rm.Start, r.End)})
			}
			cursor = max64(cursor, rm.End)
			if cursor >= r.End {
				break
			}
		}
		if cursor < r.End {
			out = append(out, Range{Start: cursor, End: r.End})
		}
	}
	return MergeRanges(out)
}

// PlanForError builds a local recovery plan. dependencies are added only when
// the damaged interval cannot be interpreted safely without them.
func PlanForError(e *auditerrors.Error, dependencies []Range, safe []Range) Plan {
	impacted := Range{Start: e.Offset, End: e.Offset + 1}
	if e.Bytes != nil {
		impacted = *e.Bytes
	}
	recompute := MergeRanges(append([]Range{impacted}, dependencies...))
	return Plan{
		Code:         string(e.Code),
		FailedAt:     e.Offset,
		Impacted:     impacted,
		Dependencies: MergeRanges(dependencies),
		Recompute:    recompute,
		Reuse:        SubtractRanges(safe, recompute),
		Stage:        "range-recovery",
	}
}

// Journal accumulates successful ranges and makes duplicate completion visible
// as one merged interval rather than repeated output entries.
type Journal struct {
	completed []Range
}

func (j *Journal) Add(r Range) {
	j.completed = MergeRanges(append(j.completed, r))
}

func (j *Journal) AddMany(rs []Range) {
	j.completed = MergeRanges(append(j.completed, rs...))
}

func (j *Journal) Ranges() []Range {
	return MergeRanges(j.completed)
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
