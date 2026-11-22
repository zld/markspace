package timeline

import (
	"fmt"
)

// Span is a half-open interval [Start, Start+Len) in ticks.
type Span struct {
	Start Ticks
	Len   Ticks
}

// End returns the exclusive end of the span.
func (s Span) End() Ticks { return s.Start + s.Len }

// TrackTimeline maps one source track's ticks onto a shared output scale.
// A non-zero StartTicks is preserved as an explicit source origin instead of
// being silently dropped, so receivers can tell where the first sample sits.
type TrackTimeline struct {
	TrackID    uint32
	Source     Scale
	Output     Scale
	StartTicks Ticks // first sample's timestamp in Source units (may be > 0)
	Duration   Ticks // decoded duration in Source units
	EditShift  Ticks // presentation shift applied by an edit list, in Source units
}

// Map converts a source tick position to the output grid.
func (t TrackTimeline) Map(v Ticks) (Ticks, error) {
	return Rebase(v, t.StartTicks-t.EditShift, t.Source, t.Output)
}

// MapSpan converts a source span to the output grid, rounding both edges
// independently so a span never drifts by accumulated error.
func (t TrackTimeline) MapSpan(s Span) (Span, error) {
	a, err := t.Map(s.Start)
	if err != nil {
		return Span{}, err
	}
	b, err := t.Map(s.Start + s.Len)
	if err != nil {
		return Span{}, err
	}
	if b < a {
		return Span{}, fmt.Errorf("timeline: negative mapped span track=%d", t.TrackID)
	}
	return Span{Start: a, Len: b - a}, nil
}

// SourceMillis converts a source timestamp into whole milliseconds.
func (t TrackTimeline) SourceMillis(v Ticks) int64 { return ToMillis(v, t.Source) }

// MapMillis converts a source timestamp into output-scale milliseconds.
func (t TrackTimeline) MapMillis(v Ticks) (int64, error) {
	m, err := t.Map(v)
	if err != nil {
		return 0, err
	}
	return ToMillis(m, t.Output), nil
}

// Segment is one contiguous run of valid audio on the shared output grid.
// Gaps are explicit: a hole between two segments is never represented as
// silence-filled samples.
type Segment struct {
	Track  uint32
	Source Span // in the track's source scale
	Out    Span // in the shared output scale
}

// BuildSegments maps a list of valid source spans, dropping nothing. It is
// the single place where scan samples become output positions.
func BuildSegments(t TrackTimeline, valid []Span) ([]Segment, error) {
	out := make([]Segment, 0, len(valid))
	for _, s := range valid {
		if s.Len <= 0 {
			continue
		}
		m, err := t.MapSpan(s)
		if err != nil {
			return nil, err
		}
		out = append(out, Segment{Track: t.TrackID, Source: s, Out: m})
	}
	return out, nil
}
