// Package ingest reads external material into the unified timeline:
// MP4 track timelines, RTP packet streams and edit/transition lists.
package ingest

import (
	"encoding/json"
	"io"

	"mediad/internal/clock"
	"mediad/internal/mederr"
)

// Segment is one clip placement on the program timeline.
type Segment struct {
	Track string `json:"track"` // source track id, e.g. "mp4:1" or "rtp:0"
	// StartNanos is where the segment begins on the program timeline.
	StartNanos int64 `json:"start_nanos"`
	// DurationNanos is the segment length; must be > 0.
	DurationNanos int64 `json:"duration_nanos"`
	// TransitionInNanos is the crossfade duration overlapping the tail
	// of the previous segment; 0 for a hard cut.
	TransitionInNanos int64 `json:"transition_in_nanos"`
}

// End returns the exclusive end of the segment on the program timeline.
func (s Segment) End() int64 { return s.StartNanos + s.DurationNanos }

// Span returns the segment's program-timeline span.
func (s Segment) Span() clock.Span { return clock.Span{Start: s.StartNanos, End: s.End()} }

// EDL is an ordered edit decision list with cut points and transitions.
type EDL struct {
	Segments []Segment `json:"segments"`
}

// ParseEDL reads an EDL from JSON. Unknown fields are rejected so a
// typo can never silently rewrite the timeline.
func ParseEDL(r io.Reader) (*EDL, error) {
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	var e EDL
	if err := dec.Decode(&e); err != nil {
		return nil, mederr.New(mederr.CatParse, "edl", -1, -1, false,
			"invalid EDL: %v", err)
	}
	return &e, nil
}

// Validate checks the list against the set of known tracks. Missing
// tracks, negative durations and contradictory boundaries are located
// to the offending track and fail; nothing is silently rewritten.
func (e *EDL) Validate(knownTracks []string) error {
	if len(e.Segments) == 0 {
		return mederr.New(mederr.CatTimeline, "edl", -1, -1, false, "empty EDL")
	}
	if len(e.Segments) > MaxSegments {
		return mederr.New(mederr.CatLimit, "edl", -1, -1, false,
			"too many segments: %d > %d", len(e.Segments), MaxSegments)
	}
	known := map[string]bool{}
	for _, t := range knownTracks {
		known[t] = true
	}
	for i, s := range e.Segments {
		if !known[s.Track] {
			return mederr.New(mederr.CatTimeline, s.Track, s.StartNanos, s.End(), false,
				"segment %d references missing track %q", i, s.Track)
		}
		if s.DurationNanos <= 0 {
			return mederr.New(mederr.CatTimeline, s.Track, s.StartNanos, s.End(), false,
				"segment %d has non-positive duration %d ns", i, s.DurationNanos)
		}
		if s.StartNanos < 0 {
			return mederr.New(mederr.CatTimeline, s.Track, s.StartNanos, s.End(), false,
				"segment %d starts before zero", i)
		}
		if s.TransitionInNanos < 0 || s.TransitionInNanos >= s.DurationNanos {
			return mederr.New(mederr.CatTimeline, s.Track, s.StartNanos, s.End(), false,
				"segment %d transition %d ns outside its own duration %d ns",
				i, s.TransitionInNanos, s.DurationNanos)
		}
		if i == 0 {
			if s.TransitionInNanos != 0 {
				return mederr.New(mederr.CatTimeline, s.Track, s.StartNanos, s.End(), false,
					"first segment cannot have an incoming transition")
			}
			continue
		}
		prev := e.Segments[i-1]
		// The segment must start exactly where the previous one ends,
		// minus the declared transition overlap. Anything else is a
		// contradictory boundary (gap or undeclared overlap).
		want := prev.End() - s.TransitionInNanos
		if s.StartNanos != want {
			return mederr.New(mederr.CatTimeline, s.Track, s.StartNanos, prev.End(), false,
				"segment %d boundary %d ns contradicts previous end %d ns with transition %d ns (want %d)",
				i, s.StartNanos, prev.End(), s.TransitionInNanos, want)
		}
		if s.TransitionInNanos > prev.DurationNanos {
			return mederr.New(mederr.CatTimeline, s.Track, s.StartNanos, prev.End(), false,
				"segment %d transition %d ns exceeds previous segment duration %d ns",
				i, s.TransitionInNanos, prev.DurationNanos)
		}
	}
	return nil
}

// TotalDuration returns the end of the last segment.
func (e *EDL) TotalDuration() int64 {
	if len(e.Segments) == 0 {
		return 0
	}
	return e.Segments[len(e.Segments)-1].End()
}
