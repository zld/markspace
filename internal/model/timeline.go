// Package model defines the container-independent media model shared by the
// probe, demux, recovery, mapping, remux and verification stages.
package model

import (
	"fmt"
	"sort"
)

// NanosPerSecond is the fixed denominator of the unified timeline.
const NanosPerSecond uint64 = 1_000_000_000

// Timebase is an integer media clock. All input timestamps are kept in their
// source timebase; nanoseconds are only a presentation/coordination view.
type Timebase struct {
	Timescale uint32 `json:"timescale"`
}

// Valid reports whether the timebase can be used for conversion.
func (t Timebase) Valid() bool { return t.Timescale > 0 }

// TickToNanos converts a non-negative tick value to nanoseconds, rounding
// down. Flooring is intentional: a reported range must never end before the
// last tick it contains.
func (t Timebase) TickToNanos(ticks uint64) int64 {
	if t.Timescale == 0 {
		return 0
	}
	return int64(ticks * NanosPerSecond / uint64(t.Timescale))
}

// NanosToTick converts nanoseconds back to the input timebase, rounding down.
func (t Timebase) NanosToTick(ns int64) uint64 {
	if ns <= 0 || t.Timescale == 0 {
		return 0
	}
	return uint64(ns) * uint64(t.Timescale) / NanosPerSecond
}

// TickError converts a nanosecond difference to input ticks. It is used by
// tests and reports to express localization error in input units.
func (t Timebase) TickError(ns int64) uint64 {
	if ns < 0 {
		ns = -ns
	}
	return t.NanosToTick(ns)
}

// Range is a half-open interval in a track timebase.
type Range struct {
	StartTick uint64
	Duration  uint64
}

// EndTick returns the exclusive end tick of the range.
func (r Range) EndTick() uint64 { return r.StartTick + r.Duration }

// NanosRange is the unified, nanosecond view of a Range.
type NanosRange struct {
	StartNS int64 `json:"start_ns"`
	EndNS   int64 `json:"end_ns"`
}

// ToNanos converts a tick range using the supplied timebase.
func (r Range) ToNanos(tb Timebase) NanosRange {
	return NanosRange{
		StartNS: tb.TickToNanos(r.StartTick),
		EndNS:   tb.TickToNanos(r.EndTick()),
	}
}

// Contains reports whether other lies fully inside r. It is the shared
// invariant checked by the timeline coordinator before a fragment commits.
func (r Range) Contains(other Range) bool {
	return other.StartTick >= r.StartTick && other.EndTick() <= r.EndTick()
}

// Segment is one determinable span in the explicit timeline. A segment that
// is not recoverable has no packet range but still occupies its declared
// interval so downstream code cannot silently close the gap.
type Segment struct {
	TrackKey    string `json:"track_key"`
	Source      string `json:"source"`
	StartTick   uint64 `json:"start_tick"`
	Duration    uint64 `json:"duration"`
	StartNS     int64  `json:"start_ns"`
	EndNS       int64  `json:"end_ns"`
	Recovered   bool   `json:"recovered"`
	Unrecovered bool   `json:"unrecovered"`
	Reason      string `json:"reason,omitempty"`
}

// TrackTimeline is the explicit timeline for one track.
type TrackTimeline struct {
	Key             string       `json:"key"`
	Container       string       `json:"container"`
	TrackID         uint32       `json:"track_id"`
	Timebase        Timebase     `json:"timebase"`
	Declared        NanosRange   `json:"declared"`
	Recovered       NanosRange   `json:"recovered"`
	Complete        bool         `json:"complete"`
	Rebuilt         bool         `json:"rebuilt"`
	Segments        []Segment    `json:"segments"`
	UnrecoveredNS   []NanosRange `json:"unrecovered_ns,omitempty"`
	LocalizationErr uint64       `json:"localization_tick_error"`
}

// Timeline is the shared explicit timeline used to coordinate parallel track
// processing and to decide whether a fragment lies on the committed timeline.
type Timeline struct {
	OriginNS int64                     `json:"origin_ns"`
	EndNS    int64                     `json:"end_ns"`
	Tracks   map[string]*TrackTimeline `json:"tracks"`
}

// New creates an empty timeline.
func New() *Timeline {
	return &Timeline{Tracks: make(map[string]*TrackTimeline)}
}

// SortedKeys returns the deterministic track order of the timeline.
func (t *Timeline) SortedKeys() []string {
	keys := make([]string, 0, len(t.Tracks))
	for k := range t.Tracks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// AddTrack registers a track and expands the global extent.
func (t *Timeline) AddTrack(tt *TrackTimeline) error {
	if tt == nil || tt.Key == "" {
		return fmt.Errorf("timeline: empty track")
	}
	if _, ok := t.Tracks[tt.Key]; ok {
		return fmt.Errorf("timeline: duplicate track %q", tt.Key)
	}
	t.Tracks[tt.Key] = tt
	if len(t.Tracks) == 1 || tt.Declared.StartNS < t.OriginNS {
		t.OriginNS = tt.Declared.StartNS
	}
	if tt.Declared.EndNS > t.EndNS {
		t.EndNS = tt.Declared.EndNS
	}
	return nil
}

// ValidateFragment enforces that a fragment is fully described by the
// explicit timeline of its track. This is the coordination point shared by
// all parallel workers.
func (t *Timeline) ValidateFragment(trackKey string, r Range) error {
	tt, ok := t.Tracks[trackKey]
	if !ok {
		return fmt.Errorf("timeline: unknown track %q", trackKey)
	}
	for _, s := range tt.Segments {
		if s.StartTick <= r.StartTick && r.EndTick() <= s.StartTick+s.Duration {
			return nil
		}
	}
	return fmt.Errorf("timeline: fragment [%d,%d) of %s is outside the explicit timeline",
		r.StartTick, r.EndTick(), trackKey)
}
