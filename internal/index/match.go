package index

import (
	"fmt"

	"mediad/internal/clock"
	"mediad/internal/errors"
	"mediad/internal/stream"
)

// Match is the verdict for one index entry against the scanned stream.
// Invalid entries are carried through with their parse reason and never
// rebound to a neighbouring frame.
type Match struct {
	Frame  uint64      `json:"frame"`
	Offset int64       `json:"offset"`
	Type   string      `json:"type"`
	Valid  bool        `json:"valid"`
	Hit    bool        `json:"hit"`
	AU     int64       `json:"au_index"` // -1 when unmatched
	Point  clock.Point `json:"point"`
	Reason string      `json:"reason,omitempty"`
}

// Report is the full matching outcome on the shared nanosecond timeline.
type Report struct {
	Matches        []Match `json:"matches"`
	Hits           int     `json:"hits"`
	Misses         int     `json:"misses"`
	HitRate        float64 `json:"hit_rate"`
	ExtraKeyframes []int64 `json:"extra_keyframes,omitempty"`
	DamagedOffsets []int64 `json:"damaged_offsets,omitempty"`
}

// MatchTimeline compares valid index entries with actual keyframe access
// units. Matching is by exact byte offset; a failed lookup stays a miss and
// is never rebound. Every hit is mapped onto the shared clock timeline.
func MatchTimeline(aus []*stream.AccessUnit, idx *Index, tb *clock.TimeBase) *Report {
	frameByOffset := make(map[int64]uint64, len(aus))
	referenced := make(map[int64]bool)
	var damaged []int64
	for _, au := range aus {
		if au.Keyframe {
			frameByOffset[au.Start] = au.Index
		}
		if au.Corrupt {
			damaged = append(damaged, au.Start)
		}
	}

	rep := &Report{DamagedOffsets: damaged}
	for _, e := range idx.Entries {
		m := Match{
			Frame: e.Raw.Frame, Offset: e.Raw.Off, Type: e.Raw.Type,
			Valid: e.Valid, AU: -1,
		}
		if !e.Valid {
			m.Reason = e.Reason
			rep.Matches = append(rep.Matches, m)
			continue // hit rate counts valid entries only
		}
		if auIdx, ok := frameByOffset[e.Raw.Off]; ok {
			m.Hit = true
			m.AU = int64(auIdx)
			m.Point = clock.Point{Frame: auIdx, Offset: tb.FrameToNS(auIdx)}
			rep.Hits++
			referenced[e.Raw.Off] = true
		} else {
			m.Reason = fmt.Sprintf("%s: no keyframe access unit at offset %d",
				errs.CodeIndexMismatch, e.Raw.Off)
			rep.Misses++
		}
		rep.Matches = append(rep.Matches, m)
	}

	for off := range frameByOffset {
		if !referenced[off] {
			rep.ExtraKeyframes = append(rep.ExtraKeyframes, off)
		}
	}
	if idx.ValidCount > 0 {
		rep.HitRate = float64(rep.Hits) / float64(idx.ValidCount)
	}
	return rep
}
