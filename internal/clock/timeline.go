package clock

import (
	"errors"
	"fmt"
	"sort"
)

// Timeline invariant violations.
var (
	ErrNegativeStart   = errors.New("clock: timeline start must not be negative")
	ErrBadDuration     = errors.New("clock: segment duration must be positive")
	ErrOverlap         = errors.New("clock: segment overlaps an earlier segment")
	ErrImplicitGap     = errors.New("clock: timeline has an unaccounted gap")
	ErrBadEditList     = errors.New("clock: edit list is malformed")
	ErrEditOutOfBounds = errors.New("clock: edit list references media outside the track")
)

// Kind distinguishes real media coverage from explicit holes.
type Kind uint8

const (
	// Media is a confirmed range of samples.
	Media Kind = iota + 1
	// Gap is an explicit interval with no usable media.
	Gap
)

func (k Kind) String() string {
	switch k {
	case Media:
		return "media"
	case Gap:
		return "gap"
	default:
		return "unknown"
	}
}

// Segment is one contiguous interval on the presentation timeline.
// Start is absolute and may be non-zero; MediaStart is the corresponding
// tick in the source media time base (zero for gaps).
type Segment struct {
	Kind       Kind
	Start      int64
	Duration   int64
	MediaStart int64
	Source     string
}

// End returns the exclusive end tick of the segment.
func (s Segment) End() int64 { return s.Start + s.Duration }

// Interval is a bare [Start, Start+Duration) range.
type Interval struct {
	Start    int64
	Duration int64
}

// End returns the exclusive end tick.
func (i Interval) End() int64 { return i.Start + i.Duration }

// Timeline is an ordered, non-overlapping set of presentation intervals that
// begins at an explicit (possibly non-zero) start tick.  Gaps must be declared
// with AddGap; silently skipping time is a validation error.
type Timeline struct {
	Rate     Rate
	start    int64
	startSet bool
	segments []Segment
	cursor   int64
}

// NewTimeline creates an empty timeline at rate r.
func NewTimeline(r Rate) (*Timeline, error) {
	if !r.IsValid() {
		return nil, ErrInvalidRate
	}
	return &Timeline{Rate: r}, nil
}

// SetStart pins the first presentation tick.  It must be called before any
// segment is appended.  A zero start is allowed but must be stated explicitly
// so callers cannot accidentally assume one.
func (t *Timeline) SetStart(v int64) error {
	if len(t.segments) != 0 {
		return errors.New("clock: start must be set before segments are appended")
	}
	if v < 0 {
		return ErrNegativeStart
	}
	t.start = v
	t.startSet = true
	t.cursor = v
	return nil
}

// Start returns the first presentation tick.
func (t *Timeline) Start() (int64, bool) { return t.start, t.startSet }

// End returns the exclusive end of everything appended so far.
func (t *Timeline) End() int64 { return t.cursor }

// Duration returns total length from start to end.
func (t *Timeline) Duration() int64 {
	if !t.startSet {
		return 0
	}
	return t.cursor - t.start
}

// Segments returns a copy of the ordered segment list.
func (t *Timeline) Segments() []Segment {
	out := make([]Segment, len(t.segments))
	copy(out, t.segments)
	return out
}

// AddSegment appends a segment at the current cursor.  It refuses zero or
// negative durations and any attempt to place media before the pinned start.
func (t *Timeline) AddSegment(seg Segment) (Segment, error) {
	if !t.startSet {
		return Segment{}, errors.New("clock: SetStart must be called first")
	}
	if seg.Duration <= 0 {
		return Segment{}, ErrBadDuration
	}
	if seg.Kind != Media && seg.Kind != Gap {
		return Segment{}, fmt.Errorf("clock: unknown segment kind %d", seg.Kind)
	}
	if seg.Start != t.cursor {
		return Segment{}, fmt.Errorf("%w: segment starts at %d, cursor is %d", ErrOverlap, seg.Start, t.cursor)
	}
	if seg.MediaStart < 0 || seg.Duration < 0 {
		return Segment{}, ErrNegative
	}
	t.segments = append(t.segments, seg)
	t.cursor = seg.End()
	return seg, nil
}

// AddMedia appends confirmed media covering mediaTicks from mediaStart.
func (t *Timeline) AddMedia(source string, mediaStart, duration int64) (Segment, error) {
	return t.AddSegment(Segment{Kind: Media, Start: t.cursor, Duration: duration, MediaStart: mediaStart, Source: source})
}

// AddGap appends an explicit hole of duration ticks.
func (t *Timeline) AddGap(duration int64) (Segment, error) {
	return t.AddSegment(Segment{Kind: Gap, Start: t.cursor, Duration: duration})
}

// Validate checks every endpoint invariant: the list starts at Start, each
// segment begins exactly where the previous one ended, durations are positive,
// and the recorded end matches the sum of durations.
func (t *Timeline) Validate() error {
	if !t.startSet {
		return errors.New("clock: start is not set")
	}
	if len(t.segments) == 0 {
		if t.cursor != t.start {
			return ErrImplicitGap
		}
		return nil
	}
	var sum int64
	want := t.start
	for i, seg := range t.segments {
		if seg.Duration <= 0 {
			return fmt.Errorf("%w at index %d", ErrBadDuration, i)
		}
		if seg.Start != want {
			if seg.Start < want {
				return fmt.Errorf("%w at index %d: start %d < expected %d", ErrOverlap, i, seg.Start, want)
			}
			return fmt.Errorf("%w at index %d: start %d > expected %d", ErrImplicitGap, i, seg.Start, want)
		}
		if seg.Kind == Media && seg.MediaStart < 0 {
			return fmt.Errorf("%w at index %d", ErrNegative, i)
		}
		sum += seg.Duration
		want = seg.End()
	}
	if want != t.cursor {
		return fmt.Errorf("clock: recorded end %d != summed end %d", t.cursor, want)
	}
	if t.cursor-t.start != sum {
		return fmt.Errorf("clock: duration %d != sum %d", t.cursor-t.start, sum)
	}
	return nil
}

// Gaps returns all explicit gap intervals.
func (t *Timeline) Gaps() []Interval {
	var out []Interval
	for _, s := range t.segments {
		if s.Kind == Gap {
			out = append(out, Interval{Start: s.Start, Duration: s.Duration})
		}
	}
	return out
}

// MediaDuration returns the sum of confirmed media only; corrupted or missing
// time is never counted as valid audio.
func (t *Timeline) MediaDuration() int64 {
	var sum int64
	for _, s := range t.segments {
		if s.Kind == Media {
			sum += s.Duration
		}
	}
	return sum
}

// MissingDuration returns the sum of explicit gaps.
func (t *Timeline) MissingDuration() int64 {
	var sum int64
	for _, s := range t.segments {
		if s.Kind == Gap {
			sum += s.Duration
		}
	}
	return sum
}

// Covered reports whether every tick in [lo, hi) is backed by media.
func (t *Timeline) Covered(lo, hi int64) bool {
	if lo >= hi {
		return true
	}
	cursor := lo
	for _, s := range t.segments {
		if s.End() <= cursor {
			continue
		}
		if s.Start > cursor {
			return false
		}
		if s.Kind == Gap {
			return false
		}
		cursor = s.End()
		if cursor >= hi {
			return true
		}
	}
	return false
}

// EditEntry is one presentation edit.  An empty edit (MediaTime = -1)
// represents an explicit gap before media starts.
type EditEntry struct {
	MediaTime int64
	Duration  int64
}

// NewEmptyEdit returns an elst entry that inserts a silent gap.
func NewEmptyEdit(duration int64) EditEntry { return EditEntry{MediaTime: -1, Duration: duration} }

// IsEmpty reports whether the entry is an empty edit.
func (e EditEntry) IsEmpty() bool { return e.MediaTime < 0 }

// EditMap maps media ticks onto presentation ticks for one track.
// MovieRate is the rate of the edit segment durations; MediaRate is the
// track's own media timescale.  Non-zero presentation starts are preserved.
type EditMap struct {
	MovieRate Rate
	MediaRate Rate
	Start     int64 // presentation tick of the first edit
	entries   []EditEntry
	mediaDur  int64 // known media duration in media ticks, -1 if unknown
}

// NewEditMap validates and stores an edit list.
// mediaDuration is the track media duration in media ticks; pass -1 when it
// cannot be confirmed yet (recovery may fill it in later).
func NewEditMap(entries []EditEntry, movieRate, mediaRate Rate, start, mediaDuration int64) (*EditMap, error) {
	if !movieRate.IsValid() || !mediaRate.IsValid() {
		return nil, ErrInvalidRate
	}
	if start < 0 {
		return nil, ErrNegativeStart
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: empty edit list", ErrBadEditList)
	}
	em := &EditMap{MovieRate: movieRate, MediaRate: mediaRate, Start: start, mediaDur: mediaDuration}
	em.entries = make([]EditEntry, len(entries))
	copy(em.entries, entries)
	for i, e := range entries {
		if e.Duration <= 0 {
			return nil, fmt.Errorf("%w: entry %d has duration %d", ErrBadEditList, i, e.Duration)
		}
		if e.IsEmpty() {
			continue
		}
		if e.MediaTime < 0 {
			return nil, fmt.Errorf("%w: entry %d has negative media time", ErrBadEditList, i)
		}
		if mediaDuration >= 0 {
			needMovie, err := Rescale(e.Duration, movieRate, mediaRate)
			if err != nil {
				return nil, err
			}
			if e.MediaTime+needMovie > mediaDuration {
				return nil, fmt.Errorf("%w: entry %d needs media [%d,%d)", ErrEditOutOfBounds, i, e.MediaTime, e.MediaTime+needMovie)
			}
		}
	}
	return em, nil
}

// Entries returns a copy of the edit list.
func (em *EditMap) Entries() []EditEntry {
	out := make([]EditEntry, len(em.entries))
	copy(out, em.entries)
	return out
}

// MediaDuration is the media tick duration used for bounds checks.
func (em *EditMap) MediaDuration() int64 { return em.mediaDur }

// PresentationEnd returns the exclusive end of the edit list.
func (em *EditMap) PresentationEnd() int64 {
	var sum int64
	for _, e := range em.entries {
		d, err := Rescale(e.Duration, em.MovieRate, em.MovieRate)
		if err != nil {
			continue
		}
		sum += d
	}
	return em.Start + sum
}

// Map converts a media tick position to a presentation tick.  It reports
// false when the position falls inside an empty edit or outside the list.
func (em *EditMap) Map(mediaTick int64) (int64, bool) {
	presCursor := em.Start
	for _, e := range em.entries {
		presDur := e.Duration
		if e.IsEmpty() {
			presCursor += presDur
			continue
		}
		mediaDur, err := Rescale(presDur, em.MovieRate, em.MediaRate)
		if err != nil {
			return 0, false
		}
		if mediaTick >= e.MediaTime && mediaTick < e.MediaTime+mediaDur {
			delta := mediaTick - e.MediaTime
			// presentation offset for delta media ticks, exact
			presDelta, err := Rescale(delta, em.MediaRate, em.MovieRate)
			if err != nil {
				return 0, false
			}
			return presCursor + presDelta, true
		}
		presCursor += presDur
	}
	return 0, false
}

// PresentationToMedia is the inverse of Map.  It reports false when the
// presentation time lands in an empty edit or past the list.
func (em *EditMap) PresentationToMedia(presTick int64) (int64, bool) {
	if presTick < em.Start {
		return 0, false
	}
	presCursor := em.Start
	for _, e := range em.entries {
		presDur := e.Duration
		if presTick < presCursor+presDur {
			if e.IsEmpty() {
				return 0, false
			}
			delta := presTick - presCursor
			mediaDelta, err := Rescale(delta, em.MovieRate, em.MediaRate)
			if err != nil {
				return 0, false
			}
			return e.MediaTime + mediaDelta, true
		}
		presCursor += presDur
	}
	return 0, false
}

// CoversPresentation reports whether the edit list covers presTick.
func (em *EditMap) CoversPresentation(presTick int64) bool {
	_, ok := em.PresentationToMedia(presTick)
	if ok {
		return true
	}
	// empty edits cover the tick but carry no media
	presCursor := em.Start
	for _, e := range em.entries {
		if presTick >= presCursor && presTick < presCursor+e.Duration {
			return true
		}
		presCursor += e.Duration
	}
	return false
}

// SortedIntervals sorts intervals by start and rejects overlaps.
func SortedIntervals(in []Interval) ([]Interval, error) {
	out := make([]Interval, len(in))
	copy(out, in)
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	for i := 1; i < len(out); i++ {
		if out[i-1].End() > out[i].Start {
			return nil, ErrOverlap
		}
	}
	return out, nil
}

// Union returns the smallest interval set covering all input intervals,
// merging adjacent or overlapping ranges.
func Union(in []Interval) []Interval {
	if len(in) == 0 {
		return nil
	}
	out := make([]Interval, 0, len(in))
	for _, iv := range in {
		if iv.Duration <= 0 {
			continue
		}
		out = append(out, iv)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Start < out[j].Start })
	merged := out[:0]
	for _, iv := range out {
		if len(merged) == 0 || merged[len(merged)-1].End() < iv.Start {
			merged = append(merged, iv)
			continue
		}
		last := &merged[len(merged)-1]
		if iv.End() > last.End() {
			last.Duration = iv.End() - last.Start
		}
	}
	return merged
}
