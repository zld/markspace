package clock

import "sort"

// TrackTimeline records the decode progress of a single track and enforces
// monotonic decode times.
type TrackTimeline struct {
	TrackID   uint32
	Timescale uint32
	Fragments uint64
	LastStart int64
	LastEnd   int64
	Started   bool
}

// ExpectedStart returns the decode time at which the next fragment should
// start, and whether any fragment has been recorded yet.
func (t *TrackTimeline) ExpectedStart() (int64, bool) { return t.LastEnd, t.Started }

// Advance records a fragment covering [start, start+dur) in track ticks.
// Decode start times must be non-decreasing across the track.
func (t *TrackTimeline) Advance(start, dur int64) error {
	if dur <= 0 {
		return &Error{Cat: CatInvariant, Track: t.TrackID,
			Msg: "fragment duration must be positive"}
	}
	if t.Started && start < t.LastStart {
		return &Error{Cat: CatInvariant, Track: t.TrackID,
			Fragment: int64(t.Fragments), Msg: "non-monotonic decode time"}
	}
	t.LastStart = start
	t.LastEnd = start + dur
	t.Started = true
	t.Fragments++
	return nil
}

// TimelineSet manages one timeline per track.
type TimelineSet struct {
	tracks map[uint32]*TrackTimeline
}

// NewTimelineSet returns an empty set.
func NewTimelineSet() *TimelineSet {
	return &TimelineSet{tracks: map[uint32]*TrackTimeline{}}
}

// Track returns the timeline for id, creating it with the given timescale
// on first use. A conflicting timescale for an existing track is an
// invariant error.
func (s *TimelineSet) Track(id uint32, timescale uint32) (*TrackTimeline, error) {
	tl, ok := s.tracks[id]
	if !ok {
		tl = &TrackTimeline{TrackID: id, Timescale: timescale}
		s.tracks[id] = tl
		return tl, nil
	}
	if tl.Timescale != timescale {
		return nil, &Error{Cat: CatInvariant, Track: id,
			Msg: "conflicting track timescale"}
	}
	return tl, nil
}

// Timelines returns all timelines sorted by track ID for deterministic
// iteration.
func (s *TimelineSet) Timelines() []*TrackTimeline {
	out := make([]*TrackTimeline, 0, len(s.tracks))
	for _, tl := range s.tracks {
		out = append(out, tl)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TrackID < out[j].TrackID })
	return out
}
