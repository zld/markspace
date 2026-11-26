// Package timeline compiles multi-track edits and transitions into canonical time.
package timeline

import (
	"fmt"

	"assetpack/internal/clock"
)

// Clip is one source interval placed on a track. All times are 90k ticks.
type Clip struct {
	ID             string
	Track          string
	Kind           string
	Start          clock.Ticks // inclusive timeline start
	SourceStart    clock.Ticks // inclusive source start
	SourceEnd      clock.Ticks // exclusive source end
	SourceDuration clock.Ticks
	Grid           clock.Ticks // optional sample/edit grid, e.g. 3750 or 15
}

func (c Clip) End() clock.Ticks { return c.Start + c.SourceEnd - c.SourceStart }
func (c Clip) Span() clock.Range {
	return clock.Range{From: c.Start, To: c.End()}
}
func (c Clip) SourceRange() clock.Range {
	return clock.Range{From: c.SourceStart, To: c.SourceEnd}
}

// Transition is a named effect spanning an edit point.
type Transition struct {
	ID        string
	Track     string
	Kind      string // cut, crossfade, dip-to-black
	FromClip  string
	ToClip    string
	EditPoint clock.Ticks
	Duration  clock.Ticks
	Grid      clock.Ticks
}

func (tr Transition) Span() clock.Range {
	half := tr.Duration / 2
	return clock.Range{From: tr.EditPoint - half, To: tr.EditPoint + half}
}

// Sequence is the deterministic edit plan for one release sequence.
type Sequence struct {
	ID          int
	Duration    clock.Ticks
	Clips       []Clip
	Transitions []Transition
}

// Element is a validated clip with its canonical timeline span.
type Element struct {
	Clip        Clip        `json:"clip"`
	Span        clock.Range `json:"span"`
	SourceRange clock.Range `json:"source_range"`
}

// CompiledTransition contains validated transition bounds.
type CompiledTransition struct {
	Transition
	Span clock.Range `json:"span"`
}

// Track is one canonical timeline track.
type Track struct {
	ID       string        `json:"id"`
	Kind     string        `json:"kind"`
	Elements []Element     `json:"elements"`
	Spans    []clock.Range `json:"spans"`
}

// Timeline is the only structure queried by indexing and rendering.
type Timeline struct {
	Sequence    int                  `json:"sequence"`
	Duration    clock.Ticks          `json:"duration"`
	Tracks      []*Track             `json:"tracks"`
	Transitions []CompiledTransition `json:"transitions,omitempty"`
	trackByID   map[string]*Track
}

// Compile validates and canonicalizes an edit sequence without float arithmetic.
func Compile(seq Sequence) (*Timeline, error) {
	if seq.Duration <= 0 {
		return nil, clock.Wrap("*", "compile-duration", 0, fmt.Errorf("sequence %d duration must be positive", seq.ID))
	}
	tl := &Timeline{Sequence: seq.ID, Duration: seq.Duration, trackByID: map[string]*Track{}}
	clipByID := map[string]Clip{}

	for i, c := range seq.Clips {
		off := int64(c.Start)
		if c.ID == "" || c.Track == "" {
			return nil, clock.Wrap(c.Track, "compile-clip-id", off, fmt.Errorf("clip[%d] missing id/track", i))
		}
		if _, dup := clipByID[c.ID]; dup {
			return nil, clock.Wrap(c.Track, "compile-clip-id", off, fmt.Errorf("duplicate clip id %s", c.ID))
		}
		sr := c.SourceRange()
		if err := sr.Validate(c.SourceDuration); err != nil {
			return nil, clock.Wrap(c.Track, "source-range", int64(c.SourceStart), err)
		}
		span := c.Span()
		if err := span.Validate(seq.Duration); err != nil {
			return nil, clock.Wrap(c.Track, "timeline-range", int64(c.Start), err)
		}
		if c.Grid > 0 {
			if c.Start%c.Grid != 0 || c.SourceStart%c.Grid != 0 || c.SourceEnd%c.Grid != 0 {
				return nil, clock.Wrap(c.Track, "align", off,
					fmt.Errorf("clip %s point is not aligned to grid %d", c.ID, c.Grid))
			}
		}
		track := tl.trackByID[c.Track]
		if track == nil {
			track = &Track{ID: c.Track, Kind: c.Kind}
			tl.trackByID[c.Track] = track
			tl.Tracks = append(tl.Tracks, track)
		}
		if len(track.Elements) > 0 {
			prev := track.Elements[len(track.Elements)-1].Clip
			if c.Start < prev.Start {
				return nil, clock.Wrap(c.Track, "order", off,
					fmt.Errorf("clip %s starts before previous clip %s", c.ID, prev.ID))
			}
			prevEnd := prev.End()
			if c.Start < prevEnd {
				return nil, clock.Wrap(c.Track, "overlap", off,
					fmt.Errorf("clip %s overlaps %s", c.ID, prev.ID))
			}
			if c.Start > prevEnd {
				return nil, clock.Wrap(c.Track, "gap", off,
					fmt.Errorf("clip %s leaves unsupported gap after %s", c.ID, prev.ID))
			}
		}
		track.Elements = append(track.Elements, Element{Clip: c, Span: span, SourceRange: sr})
		track.Spans = append(track.Spans, span)
		clipByID[c.ID] = c
	}

	for _, tr := range seq.Transitions {
		off := int64(tr.EditPoint)
		if tr.Duration < 0 {
			return nil, clock.Wrap(tr.Track, "transition-duration", off, fmt.Errorf("negative duration"))
		}
		if tr.Duration == 0 || tr.Kind == "cut" {
			tr.Duration = 0
			tl.Transitions = append(tl.Transitions, CompiledTransition{Transition: tr, Span: clock.Range{From: tr.EditPoint, To: tr.EditPoint}})
			continue
		}
		if tr.Duration%2 != 0 {
			return nil, clock.Wrap(tr.Track, "align", off, fmt.Errorf("transition %s duration %d must have an integer midpoint", tr.ID, tr.Duration))
		}
		if tr.Grid > 0 && (tr.EditPoint%tr.Grid != 0 || tr.Duration%tr.Grid != 0) {
			return nil, clock.Wrap(tr.Track, "align", off, fmt.Errorf("transition %s not aligned to grid %d", tr.ID, tr.Grid))
		}
		from, ok1 := clipByID[tr.FromClip]
		to, ok2 := clipByID[tr.ToClip]
		if !ok1 || !ok2 || from.Track != tr.Track || to.Track != tr.Track {
			return nil, clock.Wrap(tr.Track, "transition-refs", off, fmt.Errorf("transition %s has invalid clip refs", tr.ID))
		}
		if from.End() != tr.EditPoint || to.Start != tr.EditPoint {
			return nil, clock.Wrap(tr.Track, "transition-point", off,
				fmt.Errorf("transition %s edit point %d must equal %s end/%s start", tr.ID, tr.EditPoint, from.ID, to.ID))
		}
		span := tr.Span()
		if span.From < from.Start || span.To > to.End() {
			return nil, clock.Wrap(tr.Track, "transition-bounds", off, fmt.Errorf("transition %s exceeds clip handles", tr.ID))
		}
		if err := span.Validate(seq.Duration); err != nil {
			return nil, clock.Wrap(tr.Track, "transition-range", off, err)
		}
		tl.Transitions = append(tl.Transitions, CompiledTransition{Transition: tr, Span: span})
	}
	return tl, nil
}

// TrackByID returns a compiled track.
func (tl *Timeline) TrackByID(id string) *Track { return tl.trackByID[id] }
