package index

import (
	"fmt"
	"sort"

	"replaypipe/internal/timebase"
)

// Keyframe is a sync sample that can start an independent decode.
type Keyframe struct {
	Index  int   `json:"index"`
	DTS    int64 `json:"dts"`
	DTSNS  int64 `json:"dts_ns"`
	Offset int64 `json:"offset"`
	Size   uint32 `json:"size"`
}

// KeyframeIndex maps nanosecond positions to decodable sync samples of one
// video track. It answers "which keyframe at or before/after t" in O(log n).
type KeyframeIndex struct {
	TrackID uint32       `json:"track_id"`
	Base    timebase.Base `json:"-"`
	Frames  []Keyframe   `json:"frames"`
	byPTS   []int        // keyframe ordinals ordered by composition time
	ptsNS   []int64
}

// BuildKeyframeIndex extracts sync samples from a video track and orders them
// by presentation time (DTS + composition offset). Non-video tracks produce
// an empty index.
func BuildKeyframeIndex(tr *Track) (*KeyframeIndex, *timebase.Error) {
	idx := &KeyframeIndex{TrackID: tr.ID, Base: tr.Base}
	if tr.Kind != TrackVideo {
		return idx, nil
	}
	if !tr.Base.Valid() {
		return nil, timebase.Newf(timebase.CodeTimeOverflow, tr.ID, -1, "video track has no valid timebase")
	}
	for _, s := range tr.Samples {
		if !s.Sync {
			continue
		}
		dtsNS, e := tr.Base.ToNanos(s.DTS, tr.ID, s.Offset)
		if e != nil {
			return nil, e
		}
		idx.Frames = append(idx.Frames, Keyframe{
			Index: s.Index, DTS: s.DTS, DTSNS: dtsNS, Offset: s.Offset, Size: s.Size,
		})
	}
	if len(idx.Frames) == 0 {
		return idx, nil
	}
	type ptsEntry struct {
		ordinal int
		ptsNS   int64
	}
	entries := make([]ptsEntry, 0, len(idx.Frames))
	for i, k := range idx.Frames {
		smp := tr.Samples[k.Index]
		pts, e := tr.Base.ToNanos(smp.DTS+smp.CTO, tr.ID, smp.Offset)
		if e != nil {
			return nil, e
		}
		entries = append(entries, ptsEntry{ordinal: i, ptsNS: pts})
	}
	sort.SliceStable(entries, func(a, b int) bool {
		if entries[a].ptsNS != entries[b].ptsNS {
			return entries[a].ptsNS < entries[b].ptsNS
		}
		return idx.Frames[entries[a].ordinal].DTSNS < idx.Frames[entries[b].ordinal].DTSNS
	})
	idx.byPTS = make([]int, 0, len(entries))
	idx.ptsNS = make([]int64, 0, len(entries))
	for _, en := range entries {
		idx.byPTS = append(idx.byPTS, en.ordinal)
		idx.ptsNS = append(idx.ptsNS, en.ptsNS)
	}
	return idx, nil
}

// Len is the number of keyframes.
func (k *KeyframeIndex) Len() int {
	if k == nil {
		return 0
	}
	return len(k.Frames)
}

// Frame returns the keyframe at ordinal i.
func (k *KeyframeIndex) Frame(i int) Keyframe { return k.Frames[i] }

// AtOrBefore returns the latest keyframe whose presentation time is <= ns.
func (k *KeyframeIndex) AtOrBefore(ns int64) (Keyframe, bool) {
	if len(k.ptsNS) == 0 || ns < k.ptsNS[0] {
		return Keyframe{}, false
	}
	i := sort.Search(len(k.ptsNS), func(i int) bool { return k.ptsNS[i] > ns }) - 1
	return k.Frames[k.byPTS[i]], true
}

// AtOrAfter returns the earliest keyframe whose presentation time is >= ns.
func (k *KeyframeIndex) AtOrAfter(ns int64) (Keyframe, bool) {
	if len(k.ptsNS) == 0 {
		return Keyframe{}, false
	}
	i := sort.Search(len(k.ptsNS), func(i int) bool { return k.ptsNS[i] >= ns })
	if i >= len(k.ptsNS) {
		return Keyframe{}, false
	}
	return k.Frames[k.byPTS[i]], true
}

// Nearest returns the keyframe closest to ns, preferring the earlier frame on
// a tie. It is used when a cut window has no keyframe on one side.
func (k *KeyframeIndex) Nearest(ns int64) (Keyframe, bool) {
	if len(k.ptsNS) == 0 {
		return Keyframe{}, false
	}
	before, hasBefore := k.AtOrBefore(ns)
	after, hasAfter := k.AtOrAfter(ns)
	switch {
	case hasBefore && hasAfter:
		if ns-before.PTSNS() <= after.PTSNS()-ns {
			return before, true
		}
		return after, true
	case hasBefore:
		return before, true
	default:
		return after, true
	}
}

// PTSNS is the presentation timestamp of a keyframe derived from its DTS.
func (f Keyframe) PTSNS() int64 { return f.DTSNS }

// String summarises the index for verify output.
func (k *KeyframeIndex) String() string {
	if k == nil {
		return "keyframes=0"
	}
	return fmt.Sprintf("track=%d keyframes=%d", k.TrackID, len(k.Frames))
}

// HitStats reports how many requested times landed on a keyframe exactly and
// how many were resolved to an aligned keyframe.
type HitStats struct {
	Requested int
	Exact     int
	Aligned   int
	Missed    int
}

// Resolve maps each requested nanosecond time to a keyframe. Exact hits are
// counted separately from aligned (nearest) resolutions.
func (k *KeyframeIndex) Resolve(times []int64) (map[int64]Keyframe, HitStats) {
	res := map[int64]Keyframe{}
	var stats HitStats
	for _, t := range times {
		stats.Requested++
		if f, ok := k.AtOrBefore(t); ok && f.PTSNS() == t {
			res[t] = f
			stats.Exact++
			continue
		}
		f, ok := k.Nearest(t)
		if !ok {
			stats.Missed++
			continue
		}
		res[t] = f
		stats.Aligned++
	}
	return res, stats
}
