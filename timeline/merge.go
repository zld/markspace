package timeline

import (
	"math"
	"sort"
)

// Timeline is the merged, stable-ordered result on the unified nano base.
type Timeline struct {
	Tracks []Track
	Events []Event
	// Stats are deterministic counts used by cross-stage verification.
	Stats Stats
}

// Stats captures conservation counters.
type Stats struct {
	TrackCount   int
	EventCount   int
	VideoSamples int
	Duplicates   int
	UnknownMeta  int
}

// NormalizeTrack converts raw ticks of one track to the unified nanosecond
// base, handling non-zero origins, wraparound, small drift and validation.
func NormalizeTrack(tr *Track, minNS, maxNS int64) error {
	if tr.Clock.Timescale <= 0 {
		return NewError("normalize", tr.ID, 0, ReasonBadClock, nil)
	}
	c := tr.Clock
	var unwrap, lastTick, firstTick int64
	haveLast := false
	for i := range tr.Events {
		ev := &tr.Events[i]
		tick := ev.Tick
		if c.WrapModulus > 0 {
			if !haveLast {
				firstTick = tick
			} else {
				delta := tick - lastTick
				if delta < -c.WrapModulus/2 {
					unwrap += c.WrapModulus
				}
			}
			lastTick = tick
			haveLast = true
			// Anchor the first raw tick at absolute zero so a stream that starts
			// near the modulus is not mistaken for a pre-wrap value.
			tick = tick - firstTick + unwrap
		}
		ns, err := scaleToNS(c.OriginNS, tick, c.Timescale)
		if err != nil {
			return NewError("normalize", tr.ID, tick, err.Reason, err.Wrap)
		}
		if ns < 0 {
			return NewError("normalize", tr.ID, ns, ReasonNegativeTime, nil)
		}
		if ns < minNS || (maxNS != 0 && ns > maxNS) {
			return NewError("normalize", tr.ID, ns, ReasonOutOfBounds, nil)
		}
		if c.MaxDriftNS > 0 && i > 0 && ns < tr.Events[i-1].TimeNS {
			back := tr.Events[i-1].TimeNS - ns
			if back > c.MaxDriftNS && c.WrapModulus == 0 {
				return NewError("normalize", tr.ID, ns, ReasonBackwards, nil)
			}
		}
		ev.TimeNS = ns
	}
	return nil
}

// scaleToNS computes origin + tick*1e9/timescale with overflow detection.
func scaleToNS(origin, tick, timescale int64) (int64, *Error) {
	hi := tick * 1e9
	if tick != 0 && hi/tick != 1e9 {
		return 0, NewError("scale", 0, tick, ReasonOverflow, nil)
	}
	scaled := hi / timescale
	if origin > 0 && scaled > math.MaxInt64-origin {
		return 0, NewError("scale", 0, tick, ReasonOverflow, nil)
	}
	return origin + scaled, nil
}

// Merge normalizes every track and produces a stable, deduplicated timeline.
func Merge(tracks []Track, minNS, maxNS int64) (*Timeline, error) {
	if len(tracks) == 0 {
		return nil, NewError("merge", 0, 0, "no tracks", nil)
	}
	seenIDs := map[uint32]bool{}
	out := make([]Track, 0, len(tracks))
	var all []Event
	tl := &Timeline{}
	for i := range tracks {
		tr := tracks[i]
		if seenIDs[tr.ID] {
			return nil, NewError("merge", tr.ID, 0, "duplicate track id", nil)
		}
		seenIDs[tr.ID] = true
		if err := NormalizeTrack(&tr, minNS, maxNS); err != nil {
			return nil, err
		}
		for _, ev := range tr.Events {
			if ev.Kind == KindUnknown {
				tl.Stats.UnknownMeta++
			}
			if ev.Kind == KindVideo {
				tl.Stats.VideoSamples++
			}
		}
		all = append(all, tr.Events...)
		out = append(out, tr)
	}
	all, dups := dedupe(all)
	// Stable sort: time, then track id, then input sequence.
	sort.SliceStable(all, func(a, b int) bool {
		if all[a].TimeNS != all[b].TimeNS {
			return all[a].TimeNS < all[b].TimeNS
		}
		if all[a].TrackID != all[b].TrackID {
			return all[a].TrackID < all[b].TrackID
		}
		return all[a].Seq < all[b].Seq
	})
	tl.Tracks = out
	tl.Events = all
	tl.Stats.Duplicates = dups
	tl.Stats.TrackCount = len(out)
	tl.Stats.EventCount = len(all)
	return tl, nil
}

// dedupe removes exact repeats (same track, kind, time and sequence),
// keeping the first occurrence; the input order is preserved.
func dedupe(events []Event) ([]Event, int) {
	type key struct {
		track uint32
		kind  Kind
		time  int64
		seq   int64
	}
	seen := map[key]bool{}
	out := events[:0]
	dups := 0
	for _, ev := range events {
		k := key{ev.TrackID, ev.Kind, ev.TimeNS, ev.Seq}
		if seen[k] {
			dups++
			continue
		}
		seen[k] = true
		out = append(out, ev)
	}
	return out, dups
}

// EventsAt returns all events visible at time t (sorted by track then seq).
func (tl *Timeline) EventsAt(t int64) []Event {
	var res []Event
	for _, ev := range tl.Events {
		if ev.TimeNS == t {
			res = append(res, ev)
		}
	}
	return res
}
