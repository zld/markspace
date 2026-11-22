// Package clock defines the shared integer media timeline used by every
// pipeline stage. All timestamps are integer ticks at a fixed rate so that
// interval arithmetic is exact, reproducible and free of float drift.
package clock

import "fmt"

// Rate is the fixed media clock in ticks per second. One tick equals one
// audio sample at 48 kHz, which also divides common video timescales.
const Rate = 48000

// Ticks is an integer position on the shared media timeline.
type Ticks int64

// Interval is a half-open range [Start, End) on the shared timeline.
type Interval struct {
	Start Ticks
	End   Ticks
}

// Dur returns the length of the interval in ticks.
func (i Interval) Dur() Ticks { return i.End - i.Start }

// Empty reports whether the interval has zero or negative length.
func (i Interval) Empty() bool { return i.End <= i.Start }

// Contains reports whether tick t lies inside the interval.
func (i Interval) Contains(t Ticks) bool { return t >= i.Start && t < i.End }

// Overlaps reports whether two intervals share at least one tick.
func (i Interval) Overlaps(o Interval) bool {
	return i.Start < o.End && o.Start < i.End
}

// Abuts reports whether o starts exactly where i ends (gapless continuity).
func (i Interval) Abuts(o Interval) bool { return i.End == o.Start }

// Before reports whether i ends at or before o starts.
func (i Interval) Before(o Interval) bool { return i.End <= o.Start }

// Ms converts ticks to milliseconds.
func Ms(t Ticks) float64 { return float64(t) * 1000 / Rate }

// FromMs converts milliseconds to ticks, rounding to nearest.
func FromMs(ms float64) Ticks { return Ticks(ms*Rate/1000 + 0.5) }

// FromSeconds converts seconds to ticks, rounding to nearest.
func FromSeconds(s float64) Ticks { return Ticks(s*Rate + 0.5) }

// Seq16Delta returns a-b for 16-bit RTP sequence numbers, mapped into
// [-32768, 32767] so comparisons are correct across the 0xffff->0 wrap.
func Seq16Delta(a, b uint16) int32 {
	d := int32(a) - int32(b)
	switch {
	case d > 32767:
		d -= 65536
	case d < -32768:
		d += 65536
	}
	return d
}

// Seq16Less reports whether a precedes b in wrap-aware sequence order.
func Seq16Less(a, b uint16) bool { return Seq16Delta(a, b) < 0 }

// Accumulator converts frame counts between two rates using exact rational
// arithmetic with a carried remainder. The cumulative error versus the
// theoretical value in*num/den stays below one output frame, which keeps
// streaming resampling drift-free across segment boundaries.
type Accumulator struct {
	Num int64 // output rate
	Den int64 // input rate
	in  int64 // total input frames seen
	out int64 // total output frames emitted
}

// NewAccumulator builds an Accumulator for inRate -> outRate conversion.
func NewAccumulator(inRate, outRate int64) *Accumulator {
	return &Accumulator{Num: outRate, Den: inRate}
}

// Next feeds inFrames input frames and returns how many output frames are
// due, keeping the fractional remainder for the next call.
func (a *Accumulator) Next(inFrames int64) int64 {
	a.in += inFrames
	want := a.in * a.Num / a.Den
	n := want - a.out
	a.out = want
	return n
}

// Expected returns the theoretical total output frame count.
func (a *Accumulator) Expected() int64 { return a.in * a.Num / a.Den }

// Stage names used in StageError, stable across the pipeline.
const (
	StageManifest = "manifest"
	StageIngest   = "ingest"
	StageRTP      = "rtp"
	StageAudio    = "audio"
	StageMerge    = "merge"
)

// StageError is the structured error type shared by all pipeline stages. It
// pins a failure to a track and stage so cancelled or recovered work stays
// locatable after the fact.
type StageError struct {
	Track string
	Stage string
	Op    string
	Err   error
}

func (e *StageError) Error() string {
	return fmt.Sprintf("track=%s stage=%s op=%s: %v", e.Track, e.Stage, e.Op, e.Err)
}

func (e *StageError) Unwrap() error { return e.Err }
