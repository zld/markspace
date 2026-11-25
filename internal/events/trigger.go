// Package events maps timestamped match events onto the media timeline
// and computes slice windows around them.
package events

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"replay/internal/cmaf"
	"replay/internal/model"
)

// Event is one timestamped match event (e.g. a kill).
type Event struct {
	ID     string `json:"id"`
	Type   string `json:"type"`
	TimeMS int64  `json:"time_ms"`
}

// Time returns the event time as a Duration on the media time base.
func (e Event) Time() time.Duration { return time.Duration(e.TimeMS) * time.Millisecond }

type fileShape struct {
	Events []Event `json:"events"`
}

// Load reads an events JSON file: {"events": [{id, type, time_ms}, ...]}.
func Load(path string) ([]Event, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f fileShape
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("events: %s: %w", path, err)
	}
	for i, e := range f.Events {
		if e.ID == "" {
			return nil, fmt.Errorf("events: %s: event %d has empty id", path, i)
		}
		if e.TimeMS < 0 {
			return nil, fmt.Errorf("events: %s: event %s has negative time", path, e.ID)
		}
	}
	return f.Events, nil
}

// Find returns the event with the given id, or nil.
func Find(evs []Event, id string) *Event {
	for i := range evs {
		if evs[i].ID == id {
			return &evs[i]
		}
	}
	return nil
}

// Window is a half-open media time range [Start, End).
type Window struct {
	Start time.Duration
	End   time.Duration
}

// Dur returns the window length.
func (w Window) Dur() time.Duration { return w.End - w.Start }

// WindowFor computes the slice window [t-before, t+after] for ev and
// validates it against the timeline. Mismatches between event time and
// media time within tol are accepted with a warning and clamped; larger
// mismatches are rejected with an error wrapping model.ErrEventOutOfRange.
func WindowFor(ev Event, before, after time.Duration, tl *cmaf.Timeline, tol time.Duration, rep *model.Report) (Window, error) {
	t := ev.Time()
	start, end := t-before, t+after
	if start < 0 {
		start = 0
	}
	if past := t - tl.End; past > 0 {
		if past > tol {
			return Window{}, fmt.Errorf("event %s at %v is %v past media end %v (tolerance %v): %w",
				ev.ID, t, past, tl.End, tol, model.ErrEventOutOfRange)
		}
		rep.Addf(model.CodeEventMismatch, model.SevWarn, "", -1,
			"event %s is %v past media end; accepted within tolerance", ev.ID, past)
	}
	if end > tl.End {
		end = tl.End
	}
	for _, m := range tl.MissingIn(start, end) {
		d := distance(t, m)
		if d > tol {
			return Window{}, fmt.Errorf(
				"event %s window [%v, %v) hits missing media [%v, %v) (%v from event, tolerance %v): %w",
				ev.ID, start, end, m.Start, m.End, d, tol, model.ErrEventOutOfRange)
		}
		rep.Addf(model.CodeEventMismatch, model.SevWarn, "", -1,
			"event %s window clipped around missing media [%v, %v)", ev.ID, m.Start, m.End)
		if m.Start >= t {
			if m.Start < end {
				end = m.Start
			}
		} else if m.End > start {
			start = m.End
		}
	}
	if end <= start {
		return Window{}, fmt.Errorf("event %s: empty window after clamping: %w",
			ev.ID, model.ErrEventOutOfRange)
	}
	return Window{Start: start, End: end}, nil
}

// distance returns how far t lies from covered media around interval m:
// the gap to m when t is outside it, or the distance to the nearest
// covered edge when t falls inside the missing range.
func distance(t time.Duration, m cmaf.Interval) time.Duration {
	switch {
	case t < m.Start:
		return m.Start - t
	case t >= m.End:
		return t - m.End
	default:
		if t-m.Start < m.End-t {
			return t - m.Start
		}
		return m.End - t
	}
}
