// Package syncer aligns audio and video tracks on one integer nanosecond
// clock. It admits late video frames without moving output time backwards and
// reports the chosen offset together with the residual alignment error.
package syncer

import (
	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// Sample is one presentation timestamp on a track. PTS is already in
// nanoseconds; Duration is the nominal frame or packet length.
type Sample struct {
	PTS      clock.Time `json:"pts_ns"`
	Duration clock.Time `json:"duration_ns"`
}

// Track is a monotonic presentation timeline.
type Track struct {
	Kind    string   `json:"kind"`
	Samples []Sample `json:"samples"`
}

// Config controls alignment.
type Config struct {
	// MaxOffset is the largest correction the aligner will apply. An
	// alignment outside this bound is reported rather than applied.
	MaxOffset clock.Time
	// LateTolerance is how far a sample may fall behind the last admitted
	// time before it is rejected outright rather than clamped.
	LateTolerance clock.Time
}

// DefaultConfig allows corrections up to 200 ms and rejects samples more than
// one frame (40 ms) late.
func DefaultConfig() Config {
	return Config{MaxOffset: 200 * clock.Millisecond, LateTolerance: 40 * clock.Millisecond}
}

// TrackStats summarises admission on one track.
type TrackStats struct {
	Kind        string     `json:"kind"`
	Input       int        `json:"input"`
	Admitted    int        `json:"admitted"`
	Clamped     int        `json:"clamped"`
	LateDropped int        `json:"late_dropped"`
	First       clock.Time `json:"first_ns"`
	Last        clock.Time `json:"last_ns"`
	Duration    clock.Time `json:"duration_ns"`
}

// Result is the synchronisation outcome.
type Result struct {
	Video         TrackStats     `json:"video"`
	Audio         TrackStats     `json:"audio"`
	Offset        clock.Time     `json:"offset_ns"`
	Residual      clock.Time     `json:"residual_ns"`
	AppliedOffset clock.Time     `json:"applied_offset_ns"`
	Samples       int            `json:"samples"`
	Monotonicity  bool           `json:"monotonic"`
	Faults        []*fault.Fault `json:"faults,omitempty"`
}

// OffsetMs renders the offset in milliseconds.
func (r *Result) OffsetMs() float64 { return clock.ToMilliseconds(r.Offset) }

// ResidualMs renders the residual error in milliseconds.
func (r *Result) ResidualMs() float64 { return clock.ToMilliseconds(r.Residual) }
