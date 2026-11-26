// Package timeline maps event windows onto multi-track clips and transitions.
package timeline

import (
	"sort"

	"replayctl/internal/fault"
	"replayctl/internal/timebase"
)

// Clip is one half-open timeline segment with identity source mapping.
type Clip struct {
	ID         string
	Track      string
	SourcePath string
	StartMS    int64
	EndMS      int64
	SourceInMS int64
}

// Transition is a linear cross-fade between two ordered clips.
type Transition struct {
	ID      string
	FromID  string
	ToID    string
	StartMS int64
	EndMS   int64
}

// Timeline is an ordered multi-track edit decision list.
type Timeline struct {
	Clips       []Clip
	Transitions []Transition
}

// Segment is a requested window intersecting one clip.
type Segment struct {
	Clip            Clip
	TimelineStartMS int64
	TimelineEndMS   int64
	SourceStartMS   int64
	SourceEndMS     int64
}

// Mapping contains all clip intersections for a window.
type Mapping struct {
	StartMS  int64
	EndMS    int64
	Segments []Segment
}

// EventWindow returns the half-open [event-pre, event+post) millisecond window.
func EventWindow(eventMS, preMS, postMS int64) (startMS, endMS int64) {
	return eventMS - preMS, eventMS + postMS
}

// Sort normalizes clip and transition order.
func (tl *Timeline) Sort() {
	sort.SliceStable(tl.Clips, func(i, j int) bool {
		if tl.Clips[i].StartMS != tl.Clips[j].StartMS {
			return tl.Clips[i].StartMS < tl.Clips[j].StartMS
		}
		return tl.Clips[i].ID < tl.Clips[j].ID
	})
	sort.SliceStable(tl.Transitions, func(i, j int) bool {
		return tl.Transitions[i].StartMS < tl.Transitions[j].StartMS
	})
}

// MapWindow verifies coverage and maps the window to source intervals.
func (tl *Timeline) MapWindow(startMS, endMS int64) (*Mapping, error) {
	if endMS < startMS {
		return nil, fault.New(fault.TimeGap, fault.StageTimeline, "", -1, -1, "inverted window [%d,%d)", startMS, endMS)
	}
	tl.Sort()
	if err := tl.findGap(startMS, endMS); err != nil {
		return nil, err
	}
	out := &Mapping{StartMS: startMS, EndMS: endMS}
	for _, c := range tl.Clips {
		s := max(c.StartMS, startMS)
		e := min(c.EndMS, endMS)
		if s >= e {
			continue
		}
		out.Segments = append(out.Segments, Segment{
			Clip:            c,
			TimelineStartMS: s,
			TimelineEndMS:   e,
			SourceStartMS:   c.SourceInMS + (s - c.StartMS),
			SourceEndMS:     c.SourceInMS + (e - c.StartMS),
		})
	}
	return out, nil
}

func (tl *Timeline) findGap(start, end int64) error {
	type iv struct{ s, e int64 }
	var ivs []iv
	for _, c := range tl.Clips {
		s := max(c.StartMS, start)
		e := min(c.EndMS, end)
		if s < e {
			ivs = append(ivs, iv{s, e})
		}
	}
	sort.Slice(ivs, func(i, j int) bool { return ivs[i].s < ivs[j].s })
	cursor := start
	for _, x := range ivs {
		if x.s > cursor {
			return fault.New(fault.TimeGap, fault.StageTimeline, "", -1, -1, "empty clip segment [%d,%d)ms", cursor, x.s)
		}
		if x.e > cursor {
			cursor = x.e
		}
		if cursor >= end {
			return nil
		}
	}
	if cursor < end {
		return fault.New(fault.TimeGap, fault.StageTimeline, "", -1, -1, "empty clip segment [%d,%d)ms", cursor, end)
	}
	return nil
}

// ActiveAt returns clips covering t, ordered by timeline start and stable ID.
func (tl *Timeline) ActiveAt(ms int64) []Clip {
	var out []Clip
	for _, c := range tl.Clips {
		if ms >= c.StartMS && ms < c.EndMS {
			out = append(out, c)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].StartMS != out[j].StartMS {
			return out[i].StartMS < out[j].StartMS
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// TransitionAt returns the transition whose half-open interval contains ms.
func (tl *Timeline) TransitionAt(ms int64) (Transition, bool) {
	for _, tr := range tl.Transitions {
		if ms >= tr.StartMS && ms < tr.EndMS {
			return tr, true
		}
	}
	return Transition{}, false
}

// BoundsTicks converts transition milliseconds to exact integer tick bounds.
func (tr Transition) BoundsTicks(rate uint32) (startTick, endTick int64, err error) {
	startTick, err = timebase.Ms(tr.StartMS).Floor(rate)
	if err != nil {
		return 0, 0, err
	}
	endTick, err = timebase.Ms(tr.EndMS).Floor(rate)
	return startTick, endTick, err
}

// AlphaFraction returns exact linear mix numerator/denominator at tick t.
func AlphaFraction(t, start, end int64) (num, den int64) {
	den = end - start
	if den <= 0 {
		return 0, 1
	}
	num = t - start
	if num < 0 {
		num = 0
	}
	if num > den {
		num = den
	}
	return num, den
}

// LimitSample saturates an integer mix accumulator to signed 16-bit PCM range.
func LimitSample(v int64) int16 {
	if v > 32767 {
		return 32767
	}
	if v < -32768 {
		return -32768
	}
	return int16(v)
}

// MixLinear blends a and b with alpha=num/den and saturates to int16 range.
func MixLinear(a, b int16, num, den int64) int16 {
	if den <= 0 {
		return a
	}
	v := (int64(a)*(den-num) + int64(b)*num) / den
	return LimitSample(v)
}
