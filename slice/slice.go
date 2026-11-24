// Package slice turns corrected track events into fixed-duration slices.
// Boundaries land on event ticks within a configured tolerance and never
// cross an unhandled error range. Each slice reports its target and measured
// bitrate; all arithmetic stays on the shared 90 kHz timeline.
package slice

import (
	"fmt"

	"mediad/internal/clock"
	"mediad/ts"
)

// Config parameterises the slicer.
type Config struct {
	// TargetTicks is the nominal slice duration in 90 kHz ticks.
	TargetTicks int64
	// ToleranceTicks allows a boundary to land early/late by at most this much.
	ToleranceTicks int64
	// TargetBitrateBPS is the nominal stream bitrate copied onto each slice.
	TargetBitrateBPS int64
}

// Slice is one emitted output span.
type Slice struct {
	TrackID string
	Start   int64
	End     int64
	Bytes   int64
	// TargetTicks records the nominal duration so alignment error is
	// measurable rather than inferred from the actual span.
	TargetTicks      int64
	TargetBitrateBPS int64
	ActualBitrateBPS int64
}

// DurationMs reports the measured slice length in whole milliseconds.
func (s Slice) DurationMs() int64 { return clock.Ms(s.End - s.Start) }

// BoundaryErrorMs is the absolute difference between the nominal target
// duration and the measured one, rounded to milliseconds; callers require it
// to stay within 1 ms whenever the target itself is tick-aligned.
func (s Slice) BoundaryErrorMs() int64 {
	d := clock.Ms(s.End-s.Start) - clock.Ms(s.TargetTicks)
	if d < 0 {
		d = -d
	}
	return d
}

// Slicer accumulates events per track and closes slices deterministically.
type Slicer struct {
	cfg     Config
	open    map[string]*Slice
	blocked map[string]bool
	out     []Slice
	errs    []clock.RangeError
}

// New validates the config and returns a slicer.
func New(cfg Config) (*Slicer, error) {
	if cfg.TargetTicks <= 0 {
		return nil, fmt.Errorf("slice: target duration must be positive")
	}
	if cfg.ToleranceTicks < 0 || cfg.ToleranceTicks >= cfg.TargetTicks {
		return nil, fmt.Errorf("slice: tolerance must be within [0,target)")
	}
	return &Slicer{cfg: cfg, open: map[string]*Slice{}, blocked: map[string]bool{}}, nil
}

// Errors returns the structured errors that forced early boundaries.
func (s *Slicer) Errors() []clock.RangeError { return s.errs }

// Observe feeds one event with the errors known at its time. Any error range
// overlapping the event closes the current slice at the error's start, so a
// boundary never spans an unhandled error.
func (s *Slicer) Observe(ev ts.TrackEvent, errs []clock.RangeError) {
	cur, ok := s.open[ev.TrackID]
	if !ok {
		cur = &Slice{TrackID: ev.TrackID, Start: ev.Start, TargetTicks: s.cfg.TargetTicks, TargetBitrateBPS: s.cfg.TargetBitrateBPS}
		s.open[ev.TrackID] = cur
	}
	for _, e := range errs {
		if e.Start < ev.End && ev.Start < e.End && e.Start > cur.Start {
			s.closeAt(ev.TrackID, e.Start)
			s.errs = append(s.errs, e)
			s.blocked[ev.TrackID] = true
			return
		}
	}
	cur = s.open[ev.TrackID]
	if cur == nil {
		// A boundary just closed due to an error; the event starts a fresh slice.
		cur = &Slice{TrackID: ev.TrackID, Start: ev.Start, TargetTicks: s.cfg.TargetTicks, TargetBitrateBPS: s.cfg.TargetBitrateBPS}
		s.open[ev.TrackID] = cur
		s.blocked[ev.TrackID] = false
	}
	cur.End = ev.End
	cur.Bytes += int64(len(ev.Payload))
	duration := cur.End - cur.Start
	if duration >= s.cfg.TargetTicks-s.cfg.ToleranceTicks {
		s.closeAt(ev.TrackID, cur.End)
	}
}

func (s *Slicer) closeAt(track string, boundary int64) {
	cur, ok := s.open[track]
	if !ok || boundary <= cur.Start {
		delete(s.open, track)
		return
	}
	cur.End = boundary
	cur.ActualBitrateBPS = bitrate(cur.Bytes, cur.End-cur.Start)
	s.out = append(s.out, *cur)
	delete(s.open, track)
}

// Flush closes any open slice at the stream end and returns all slices.
func (s *Slicer) Flush() []Slice {
	for track, cur := range s.open {
		cur.ActualBitrateBPS = bitrate(cur.Bytes, cur.End-cur.Start)
		s.out = append(s.out, *cur)
		delete(s.open, track)
	}
	return s.out
}

// bitrate converts (bytes, ticks) to bits per second on the 90 kHz clock.
func bitrate(bytes, ticks int64) int64 {
	if ticks <= 0 {
		return 0
	}
	return bytes * 8 * clock.Rate / ticks
}
