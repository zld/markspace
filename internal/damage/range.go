// Package damage merges damaged timeline ranges from all subsystems into a
// canonical, non-overlapping set per track.
package damage

import (
	"sort"
	"strings"

	"continuity/internal/timeline"
)

// Merge sorts ranges by (track, start tick) and merges overlapping or
// abutting ranges on the same track. Stream positions (sequence numbers,
// sample offsets) and tick bounds are preserved across the merge; distinct
// reasons are concatenated.
func Merge(rs []timeline.Range) []timeline.Range {
	if len(rs) == 0 {
		return nil
	}
	sorted := append([]timeline.Range(nil), rs...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Track != sorted[j].Track {
			return sorted[i].Track < sorted[j].Track
		}
		return sorted[i].Start < sorted[j].Start
	})
	out := []timeline.Range{sorted[0]}
	for _, r := range sorted[1:] {
		last := &out[len(out)-1]
		if r.Track != last.Track || r.Start > last.End {
			out = append(out, r)
			continue
		}
		if r.End > last.End {
			last.End = r.End
		}
		if r.EndSeq > last.EndSeq {
			last.EndSeq = r.EndSeq
		}
		if last.StartSeq == 0 || (r.StartSeq != 0 && r.StartSeq < last.StartSeq) {
			last.StartSeq = r.StartSeq
		}
		if r.StartOffset < last.StartOffset {
			last.StartOffset = r.StartOffset
		}
		if r.EndOffset > last.EndOffset {
			last.EndOffset = r.EndOffset
		}
		if !strings.Contains(last.Reason, r.Reason) {
			last.Reason += ";" + r.Reason
		}
	}
	return out
}
