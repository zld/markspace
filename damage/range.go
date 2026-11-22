package damage

import (
	"sort"
	"strings"
)

// Range is a damaged interval with classification. Byte offsets refer
// to the input stream; ticks refer to the media timeline.
type Range struct {
	ByteStart int64
	ByteEnd   int64 // exclusive
	TickStart int64
	TickEnd   int64 // exclusive
	Class     Class
	Detail    string
}

// Recoverable reports whether a consumer can resume after this range.
func (r Range) Recoverable() bool { return r.Class.Recoverable() }

// Merge sorts ranges by byte offset and coalesces overlapping or
// touching intervals. The merged range keeps the highest-priority
// (lowest-valued) class and joins details. The result never contains
// overlapping ranges.
func Merge(rs []Range) []Range {
	if len(rs) == 0 {
		return nil
	}
	sorted := make([]Range, len(rs))
	copy(sorted, rs)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].ByteStart != sorted[j].ByteStart {
			return sorted[i].ByteStart < sorted[j].ByteStart
		}
		return sorted[i].ByteEnd < sorted[j].ByteEnd
	})
	out := []Range{sorted[0]}
	for _, r := range sorted[1:] {
		cur := &out[len(out)-1]
		if r.ByteStart > cur.ByteEnd {
			out = append(out, r)
			continue
		}
		if r.ByteEnd > cur.ByteEnd {
			cur.ByteEnd = r.ByteEnd
		}
		if r.TickStart < cur.TickStart {
			cur.TickStart = r.TickStart
		}
		if r.TickEnd > cur.TickEnd {
			cur.TickEnd = r.TickEnd
		}
		if r.Class < cur.Class {
			cur.Class = r.Class
		}
		if r.Detail != "" && !strings.Contains(cur.Detail, r.Detail) {
			if cur.Detail != "" {
				cur.Detail += "; "
			}
			cur.Detail += r.Detail
		}
	}
	return out
}

// Overlaps reports whether any byte ranges overlap (invariant check).
func Overlaps(rs []Range) bool {
	sorted := Merge(rs)
	for i := 1; i < len(sorted); i++ {
		if sorted[i].ByteStart < sorted[i-1].ByteEnd {
			return true
		}
	}
	return false
}
