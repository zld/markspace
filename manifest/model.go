// Package manifest parses DASH MPDs and HLS playlists into one normalized
// rational-time timeline shared by the whole pipeline.
package manifest

import "streamcheck/core"

// Segment is one normalized media segment on the unified timeline.
type Segment struct {
	Index         int
	Start         core.Rat
	Dur           core.Rat
	URI           string
	Discontinuity bool // opens a new continuity interval; never a gap
	Interval      int  // continuity interval number, set by Analyze
}

// End returns the exclusive end time of the segment.
func (s Segment) End() core.Rat { return s.Start.Add(s.Dur) }

// Representation is one DASH representation or HLS rendition playlist.
type Representation struct {
	ID        string
	Bandwidth int64
	Segments  []Segment
}

// Timeline is the normalized manifest: every representation of one source.
type Timeline struct {
	Source string // "dash" or "hls"
	Reps   []Representation
}

// Jump records an unexplained timing discontinuity: a segment whose start
// does not equal the previous segment's end and is not marked as a
// discontinuity.
type Jump struct {
	Rep   string
	Index int
	Want  core.Rat // expected start (previous end)
	Got   core.Rat // declared start
}

// Analyze numbers continuity intervals and returns every unexplained
// timing jump. A discontinuity-marked segment opens a new interval and is
// never reported as a missing segment.
func (t *Timeline) Analyze() []Jump {
	var jumps []Jump
	for ri := range t.Reps {
		rep := &t.Reps[ri]
		interval := 0
		for i := range rep.Segments {
			seg := &rep.Segments[i]
			if i > 0 && seg.Discontinuity {
				interval++
			}
			seg.Interval = interval
			if i == 0 || seg.Discontinuity {
				continue
			}
			prev := rep.Segments[i-1]
			if seg.Start.Cmp(prev.End()) != 0 {
				jumps = append(jumps, Jump{
					Rep:   rep.ID,
					Index: seg.Index,
					Want:  prev.End(),
					Got:   seg.Start,
				})
			}
		}
	}
	return jumps
}

// Intervals returns the number of continuity intervals in a representation.
func (r *Representation) Intervals() int {
	max := 0
	for _, s := range r.Segments {
		if s.Interval > max {
			max = s.Interval
		}
	}
	if len(r.Segments) == 0 {
		return 0
	}
	return max + 1
}

// Discontinuities counts discontinuity-marked segments.
func (r *Representation) Discontinuities() int {
	n := 0
	for _, s := range r.Segments {
		if s.Discontinuity {
			n++
		}
	}
	return n
}

// Mismatch describes the first segment interval where two representations
// of the same content disagree.
type Mismatch struct {
	Index int
	Field string // "count", "start" or "duration"
	A     Segment
	B     Segment
}

// CompareReps checks segment count, boundaries and durations across two
// representations and returns the first mismatch, or nil if they agree.
func CompareReps(a, b Representation) *Mismatch {
	if len(a.Segments) != len(b.Segments) {
		return &Mismatch{Field: "count", Index: min(len(a.Segments), len(b.Segments))}
	}
	for i := range a.Segments {
		sa, sb := a.Segments[i], b.Segments[i]
		if sa.Start.Cmp(sb.Start) != 0 {
			return &Mismatch{Field: "start", Index: i, A: sa, B: sb}
		}
		if sa.Dur.Cmp(sb.Dur) != 0 {
			return &Mismatch{Field: "duration", Index: i, A: sa, B: sb}
		}
	}
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
