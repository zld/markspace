// Package segment builds fault-centered candidate ranges, merges overlaps
// and snaps boundaries to nearby keyframes.
package segment

import (
	"sort"

	"pwflow/internal/timebase"
)

const (
	DefaultPreNS       int64 = 2_000_000_000
	DefaultPostNS      int64 = 3_000_000_000
	DefaultKeyWindowNS int64 = 500_000_000
)

// Event is a playback-fault timestamp on the common nanosecond timeline.
type Event struct {
	ID   string `json:"id"`
	AtNS int64  `json:"at_ns"`
	Kind string `json:"kind,omitempty"`
}

// Cut is one merged candidate and its chosen actual boundaries.
type Cut struct {
	Index             int                    `json:"index"`
	FaultIDs          []string               `json:"fault_ids"`
	Requested         timebase.Interval      `json:"requested"`
	Actual            timebase.Interval      `json:"actual"`
	StartKeyframeNS   *int64                 `json:"start_keyframe_ns,omitempty"`
	EndKeyframeNS     *int64                 `json:"end_keyframe_ns,omitempty"`
	StartErrorNS      int64                  `json:"start_error_ns"`
	EndErrorNS        int64                  `json:"end_error_ns"`
	MaxErrorNS        int64                  `json:"max_error_ns"`
	StartKeyframeMiss bool                   `json:"start_keyframe_miss"`
	EndKeyframeMiss   bool                   `json:"end_keyframe_miss"`
	Metadata          map[string]interface{} `json:"metadata,omitempty"`
}

// Candidate is an interval plus the fault IDs that contributed to it.
type Candidate struct {
	Interval timebase.Interval
	FaultIDs []string
}

// BuildCandidates creates [fault-pre, fault+post) ranges, clamps them to the
// media duration and merges strictly overlapping ranges while preserving
// fault IDs.
func BuildCandidates(events []Event, durationNS, preNS, postNS int64) []Candidate {
	if preNS == 0 {
		preNS = DefaultPreNS
	}
	if postNS == 0 {
		postNS = DefaultPostNS
	}
	type raw struct {
		iv timebase.Interval
		id string
	}
	var rs []raw
	for _, e := range events {
		lo := e.AtNS - preNS
		hi := e.AtNS + postNS
		if lo < 0 {
			lo = 0
		}
		if durationNS > 0 && hi > durationNS {
			hi = durationNS
		}
		if hi <= lo {
			continue
		}
		rs = append(rs, raw{iv: timebase.Interval{Lo: lo, Hi: hi}, id: e.ID})
	}
	sort.Slice(rs, func(i, j int) bool { return rs[i].iv.Lo < rs[j].iv.Lo })

	var out []Candidate
	for _, r := range rs {
		if len(out) == 0 || r.iv.Lo >= out[len(out)-1].Interval.Hi {
			out = append(out, Candidate{Interval: r.iv, FaultIDs: []string{r.id}})
			continue
		}
		last := &out[len(out)-1]
		if r.iv.Hi > last.Interval.Hi {
			last.Interval.Hi = r.iv.Hi
		}
		last.FaultIDs = appendUnique(last.FaultIDs, r.id)
	}
	return out
}

// Snap selects the nearest keyframe on each endpoint when its distance is at
// most windowNS. Otherwise the requested endpoint is retained and the
// nearest-keyframe distance is reported as an error/miss.
func Snap(candidates []Candidate, keyframes []int64, windowNS int64) []Cut {
	if windowNS <= 0 {
		windowNS = DefaultKeyWindowNS
	}
	kf := append([]int64(nil), keyframes...)
	sort.Slice(kf, func(i, j int) bool { return kf[i] < kf[j] })
	cuts := make([]Cut, 0, len(candidates))
	for i, c := range candidates {
		start, startK, startDist, startMiss := snapEndpoint(c.Interval.Lo, kf, windowNS)
		end, endK, endDist, endMiss := snapEndpoint(c.Interval.Hi, kf, windowNS)
		if end < start {
			end = start
		}
		maxErr := startDist
		if endDist > maxErr {
			maxErr = endDist
		}
		cut := Cut{
			Index: i, FaultIDs: append([]string(nil), c.FaultIDs...),
			Requested:    c.Interval,
			Actual:       timebase.Interval{Lo: start, Hi: end},
			StartErrorNS: startDist, EndErrorNS: endDist, MaxErrorNS: maxErr,
			StartKeyframeMiss: startMiss, EndKeyframeMiss: endMiss,
		}
		if !startMiss && startK != nil {
			v := *startK
			cut.StartKeyframeNS = &v
		}
		if !endMiss && endK != nil {
			v := *endK
			cut.EndKeyframeNS = &v
		}
		cuts = append(cuts, cut)
	}
	return cuts
}

func snapEndpoint(target int64, keyframes []int64, window int64) (actual int64, kf *int64, nearestDist int64, miss bool) {
	if len(keyframes) == 0 {
		return target, nil, window + 1, true
	}
	nearest := keyframes[0]
	best := abs64(keyframes[0] - target)
	for _, k := range keyframes[1:] {
		d := abs64(k - target)
		if d < best {
			best, nearest = d, k
		}
	}
	if best <= window {
		v := nearest
		return nearest, &v, best, false
	}
	// Beyond the safe keyframe window: retain the actual requested boundary,
	// but report how far away the nearest keyframe is.
	return target, nil, best, true
}

func appendUnique(xs []string, x string) []string {
	for _, v := range xs {
		if v == x {
			return xs
		}
	}
	if x == "" {
		return xs
	}
	return append(xs, x)
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
