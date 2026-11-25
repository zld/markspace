// Package model defines the shared integer media clock and the structured
// investigation records used by every analysis stage.
package model

import (
	"errors"
	"fmt"
	"sort"
)

// Tick is the single media time unit used across RTP and MP4 analysis.
// One Tick is one microsecond, which represents Opus (48 kHz), AAC
// (32/44.1/48 kHz) and MP4 track timescales without floating point drift.
type Tick int64

const (
	// TickSecond is the number of ticks in one second.
	TickSecond Tick = 1_000_000
	// TickMilli is the number of ticks in one millisecond.
	TickMilli Tick = 1_000
)

// ErrInvalidTimescale is returned when a sample rate or timescale is zero.
var ErrInvalidTimescale = errors.New("invalid timescale")

// TicksFromSamples converts an integer sample count at rate to ticks.
// The result is rounded half up, so repeated conversions are deterministic.
func TicksFromSamples(samples int64, rate uint32) (Tick, error) {
	if rate == 0 {
		return 0, ErrInvalidTimescale
	}
	if samples < 0 {
		return 0, fmt.Errorf("negative sample count: %d", samples)
	}
	return Tick((samples*int64(TickSecond) + int64(rate)/2) / int64(rate)), nil
}

// TicksFromMillis converts milliseconds to ticks.
func TicksFromMillis(ms int64) Tick {
	return Tick(ms) * TickMilli
}

// TicksFromSeconds converts whole seconds to ticks.
func TicksFromSeconds(s int64) Tick {
	return Tick(s) * TickSecond
}

// TicksFromTimescale converts a duration expressed in a media timescale to
// ticks, rounding half up.
func TicksFromTimescale(value int64, timescale uint32) (Tick, error) {
	return TicksFromSamples(value, timescale)
}

// String renders the tick count with a microsecond suffix.
func (t Tick) String() string {
	return fmt.Sprintf("%dus", int64(t))
}

// Span is a half-open interval [Start, End) in ticks.
type Span struct {
	Start Tick `json:"start"`
	End   Tick `json:"end"`
}

// NewSpan returns a span clamped so that End is never before Start.
func NewSpan(start, end Tick) Span {
	if end < start {
		end = start
	}
	return Span{Start: start, End: end}
}

// Valid reports whether the span has a non-negative extent.
func (s Span) Valid() bool {
	return s.End >= s.Start
}

// Duration returns the span length in ticks.
func (s Span) Duration() Tick {
	if !s.Valid() {
		return 0
	}
	return s.End - s.Start
}

// Contains reports whether t lies inside the half-open span.
func (s Span) Contains(t Tick) bool {
	return t >= s.Start && t < s.End
}

// Overlaps reports whether two half-open spans intersect.
func (s Span) Overlaps(o Span) bool {
	return s.Start < o.End && o.Start < s.End
}

// Union returns the smallest span covering both inputs.
func (s Span) Union(o Span) Span {
	out := s
	if o.Start < out.Start {
		out.Start = o.Start
	}
	if o.End > out.End {
		out.End = o.End
	}
	return out
}

// MergeSpans unions overlapping or adjacent spans and returns a sorted list.
func MergeSpans(spans []Span) []Span {
	if len(spans) == 0 {
		return nil
	}
	out := append([]Span(nil), spans...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Start != out[j].Start {
			return out[i].Start < out[j].Start
		}
		return out[i].End < out[j].End
	})
	merged := out[:1]
	for _, s := range out[1:] {
		last := &merged[len(merged)-1]
		if s.Start <= last.End {
			if s.End > last.End {
				last.End = s.End
			}
			continue
		}
		merged = append(merged, s)
	}
	return merged
}
