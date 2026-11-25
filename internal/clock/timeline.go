// Package clock provides deterministic media time conversion and edit-list mapping.
package clock

import (
	"fmt"
	"math"

	"github.com/pion/rtp"

	"mediaflow/internal/fault"
)

// Timebase is a rational media time scale: ticks per second.
type Timebase struct {
	Timescale uint32 `json:"timescale"`
}

// NewTimebase rejects zero and overflow-prone time scales.
func NewTimebase(timescale uint32) (Timebase, error) {
	if timescale == 0 {
		return Timebase{}, fault.New(fault.ClassTimeBase, "timescale must be positive")
	}
	return Timebase{Timescale: timescale}, nil
}

// TicksToMS converts media ticks to integer milliseconds using floor division.
func (t Timebase) TicksToMS(ticks int64) (int64, error) {
	if t.Timescale == 0 {
		return 0, fault.New(fault.ClassTimeBase, "nil timebase")
	}
	if ticks == math.MinInt64 {
		return 0, fault.New(fault.ClassTimeBase, "timestamp is outside int64 millisecond range")
	}
	negative := ticks < 0
	if negative {
		ticks = -ticks
	}
	seconds := ticks / int64(t.Timescale)
	rem := ticks % int64(t.Timescale)
	if seconds > (math.MaxInt64-rem*1000)/1000 {
		return 0, fault.New(fault.ClassTimeBase, "millisecond conversion overflow")
	}
	ms := seconds*1000 + (rem*1000)/int64(t.Timescale)
	if negative {
		ms = -ms
	}
	return ms, nil
}

// MStoTicks converts milliseconds to media ticks.
func (t Timebase) MStoTicks(ms int64) (int64, error) {
	if t.Timescale == 0 {
		return 0, fault.New(fault.ClassTimeBase, "nil timebase")
	}
	if ms > math.MaxInt64/int64(t.Timescale)/1000 {
		return 0, fault.New(fault.ClassTimeBase, "tick conversion overflow")
	}
	return ms * int64(t.Timescale) / 1000, nil
}

// ConvertRTP converts a 32-bit RTP timestamp into ticks at another media scale.
// The RTP clock rate is carried separately because rtp.Header does not store SDP fmtp data.
func ConvertRTP(timestamp uint32, clockRate, mediaScale uint32) (int64, error) {
	if clockRate == 0 || mediaScale == 0 {
		return 0, fault.New(fault.ClassTimeBase, "clock rates must be positive")
	}
	// uint64 preserves the exact product before division.
	v := uint64(timestamp) * uint64(mediaScale) / uint64(clockRate)
	if v > math.MaxInt64 {
		return 0, fault.New(fault.ClassTimeBase, "RTP conversion overflows int64")
	}
	return int64(v), nil
}

// RTPHeaderToMS validates a parsed RTP header boundary and maps its timestamp to milliseconds.
func RTPHeaderToMS(h rtp.Header, clockRate uint32) (int64, error) {
	if clockRate == 0 {
		return 0, fault.New(fault.ClassTimeBase, "RTP clock rate must be positive")
	}
	ms := uint64(h.Timestamp) * 1000 / uint64(clockRate)
	if ms > math.MaxInt64 {
		return 0, fault.New(fault.ClassTimeBase, "RTP millisecond conversion overflow")
	}
	return int64(ms), nil
}

// RTPBoundary describes one adjacent RTP timestamp pair, including wraparound.
type RTPBoundary struct {
	Wrapped bool   `json:"wrapped"`
	Delta   uint64 `json:"delta"`
}

// CheckRTPBoundary returns the modular forward delta from prev to curr.
// A backward jump larger than maxBackward is rejected instead of being treated as wrap.
func CheckRTPBoundary(prev, curr uint32, maxBackward uint32) (RTPBoundary, error) {
	if curr >= prev {
		return RTPBoundary{Delta: uint64(curr - prev)}, nil
	}
	backward := prev - curr
	if backward <= maxBackward {
		return RTPBoundary{}, fault.New(fault.ClassTimeBase,
			"RTP timestamp moved backward by %d ticks", backward)
	}
	delta := uint64(math.MaxUint32-prev) + uint64(curr) + 1
	return RTPBoundary{Wrapped: true, Delta: delta}, nil
}

// Edit is one ISO-BMFF edit list entry in movie time units.
type Edit struct {
	Duration      int64 `json:"duration"`
	MediaTime     int64 `json:"media_time"` // -1 is an empty edit.
	MediaRateInt  int16 `json:"media_rate_int"`
	MediaRateFrac int16 `json:"media_rate_frac"`
}

func (e Edit) rate() float64 {
	integer := e.MediaRateInt
	if integer == 0 {
		integer = 1
	}
	return float64(integer) + float64(e.MediaRateFrac)/65536.0
}

// Timeline contains the minimum timing information needed for one track.
type Timeline struct {
	TrackID         uint32 `json:"track_id"`
	TrackNumber     int    `json:"track_number"`
	MediaTimescale  uint32 `json:"media_timescale"`
	MovieTimescale  uint32 `json:"movie_timescale"`
	MediaDuration   int64  `json:"media_duration"`
	FirstSampleTick int64  `json:"first_sample_tick"`
	Edits           []Edit `json:"edits,omitempty"`
}

// MediaTimebase returns the track media timebase.
func (t Timeline) MediaTimebase() Timebase { return Timebase{Timescale: t.MediaTimescale} }

// MovieTimebase returns the movie timebase, falling back to the media scale.
func (t Timeline) MovieTimebase() Timebase {
	if t.MovieTimescale != 0 {
		return Timebase{Timescale: t.MovieTimescale}
	}
	return Timebase{Timescale: t.MediaTimescale}
}

// EditMediaDuration converts an edit's movie-duration to media ticks.
func (t Timeline) EditMediaDuration(e Edit) int64 {
	movie := t.MovieTimebase()
	if movie.Timescale == 0 || t.MediaTimescale == 0 || e.Duration <= 0 {
		return 0
	}
	// Duration(movie ticks) / movieScale * mediaScale / rate.
	v := float64(e.Duration) * float64(t.MediaTimescale) /
		float64(movie.Timescale) / e.rate()
	return int64(v)
}

// ValidateEdits checks edit-list ranges against track duration and returns located faults.
func (t Timeline) ValidateEdits() []fault.Interval {
	var out []fault.Interval
	tb := t.MediaTimebase()
	for i, e := range t.Edits {
		if e.Duration <= 0 {
			out = append(out, t.interval(fault.ClassEditList, "edit %d has non-positive duration", i))
			continue
		}
		if e.MediaTime < 0 {
			continue // intentional empty edit
		}
		span := t.EditMediaDuration(e)
		end := e.MediaTime + span
		if t.MediaDuration > 0 && end > t.MediaDuration {
			startMS, _ := tb.TicksToMS(e.MediaTime)
			endMS, _ := tb.TicksToMS(end)
			out = append(out, fault.Interval{
				Kind: "edit_out_of_bounds", Class: fault.ClassEditList,
				TrackID: t.TrackID, TrackNumber: t.TrackNumber,
				StartSample: int64(i), EndSample: int64(i + 1),
				StartMS: startMS, EndMS: endMS, Reject: true,
				Message: fmt.Sprintf("edit %d ends at media tick %d beyond duration %d", i, end, t.MediaDuration),
			})
		}
	}
	return out
}

func (t Timeline) interval(class fault.Class, format string, args ...any) fault.Interval {
	return fault.Interval{Class: class, TrackID: t.TrackID, TrackNumber: t.TrackNumber,
		Reject: true, Message: fmt.Sprintf(format, args...)}
}

// PresentationMS maps a media tick through the first containing non-empty edit.
// Without an edit list it returns the raw media time on the unified timeline.
func (t Timeline) PresentationMS(mediaTick int64) (int64, int, bool, error) {
	if len(t.Edits) == 0 {
		ms, err := t.MediaTimebase().TicksToMS(mediaTick)
		return ms, -1, err == nil, err
	}
	movieScale := t.MovieTimebase().Timescale
	var cursorMovie int64
	for i, e := range t.Edits {
		spanMedia := t.EditMediaDuration(e)
		if e.MediaTime >= 0 && mediaTick >= e.MediaTime && mediaTick <= e.MediaTime+spanMedia {
			offset := mediaTick - e.MediaTime
			presMovie := cursorMovie + int64(float64(offset)*float64(movieScale)/float64(t.MediaTimescale)*e.rate())
			ms, err := t.MovieTimebase().TicksToMS(presMovie)
			return ms, i, true, err
		}
		cursorMovie += e.Duration
	}
	return 0, -1, false, fault.New(fault.ClassEditList, "media tick %d is outside every edit", mediaTick)
}

// AbsoluteMS maps a raw sample tick without subtracting the nonzero origin.
func (t Timeline) AbsoluteMS(tick int64) (int64, error) {
	return t.MediaTimebase().TicksToMS(tick)
}

// NormalizedMS subtracts FirstSampleTick so output timelines start at zero.
func (t Timeline) NormalizedMS(tick int64) (int64, error) {
	return t.MediaTimebase().TicksToMS(tick - t.FirstSampleTick)
}
