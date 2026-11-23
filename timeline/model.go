// Package timeline defines the unified nanosecond time base, tracks, events
// and the contextual error model shared by every pipeline stage.
package timeline

import (
	"errors"
	"fmt"
)

// Kind enumerates the cross-track event families the pipeline understands.
type Kind string

const (
	KindVideo    Kind = "video"
	KindSubtitle Kind = "subtitle"
	KindChapter  Kind = "chapter"
	KindID3      Kind = "id3"
	KindEmsg     Kind = "emsg"
	KindSEI      Kind = "sei"
	KindUnknown  Kind = "unknown" // preserved raw metadata, never dropped
)

// Event is a single timed item on the unified nanosecond time base.
type Event struct {
	TrackID  uint32
	Kind     Kind
	TimeNS   int64 // unified time base, nanoseconds (filled by NormalizeTrack)
	Tick     int64 // raw tick on the source clock (input to normalization)
	Seq      int64 // input sequence number on the originating track
	Payload  []byte
	Text     string
	Keyframe bool
	Size     int  // carried sample size in bytes (bitrate accounting)
	Corrupt  bool // marked by adapters when payload validation fails
	MetaType string
}

// VideoSample describes a video sampling point derived from container index.
type VideoSample struct {
	TimeNS     int64
	DurationNS int64
	Keyframe   bool
	Size       int
	Seq        int64
}

// UnknownMeta retains raw bytes and time for metadata no adapter understands.
type UnknownMeta struct {
	MetaType string
	TimeNS   int64
	Raw      []byte
}

// Clock describes a source track time base. Ticks are converted to the unified
// nanosecond base as: ns = OriginNS + ticks*1e9/Timescale.
type Clock struct {
	Timescale   int64 // ticks per second, must be > 0
	OriginNS    int64 // unified time of tick zero (may be non-zero, >= 0)
	WrapModulus int64 // 0 disables wrap handling; e.g. 1<<32 for RTP/MP4 v0
	MaxDriftNS  int64 // tolerated backward step before an error is raised
}

// Track is a parsed input track on the unified time base.
type Track struct {
	ID     uint32
	Kind   Kind
	Clock  Clock
	Events []Event
}

// Error carries track and time context so failures stay traceable across stages.
type Error struct {
	TrackID uint32
	TimeNS  int64
	Op      string
	Reason  string
	Wrap    error
}

func (e *Error) Error() string {
	if e.TrackID != 0 || e.TimeNS != 0 || e.Op != "" {
		return fmt.Sprintf("timeline: op=%s track=%d time_ns=%d: %s",
			e.Op, e.TrackID, e.TimeNS, e.Reason)
	}
	return "timeline: " + e.Reason
}

func (e *Error) Unwrap() error { return e.Wrap }

// NewError builds a contextual timeline error. When wrap is nil the reason is
// mapped onto a package sentinel so errors.Is keeps working.
func NewError(op string, trackID uint32, timeNS int64, reason string, wrap error) *Error {
	if wrap == nil {
		switch reason {
		case ReasonNegativeTime:
			wrap = ErrNegative
		case ReasonOverflow:
			wrap = ErrOverflow
		case ReasonOutOfBounds:
			wrap = ErrOutOfBounds
		case ReasonWrap:
			wrap = ErrWrap
		case ReasonBackwards:
			wrap = ErrBackwards
		case ReasonBadClock:
			wrap = ErrBadClock
		}
	}
	return &Error{Op: op, TrackID: trackID, TimeNS: timeNS, Reason: reason, Wrap: wrap}
}

// Sentinel errors shared across packages for errors.Is checks.
var (
	ErrNegative    = errors.New(ReasonNegativeTime)
	ErrOverflow    = errors.New(ReasonOverflow)
	ErrOutOfBounds = errors.New(ReasonOutOfBounds)
	ErrWrap        = errors.New(ReasonWrap)
	ErrBackwards   = errors.New(ReasonBackwards)
	ErrBadClock    = errors.New(ReasonBadClock)
)

// Sentinel reason strings shared across packages.
const (
	ReasonNegativeTime = "negative unified time"
	ReasonOverflow     = "timestamp overflow"
	ReasonOutOfBounds  = "event outside timeline bounds"
	ReasonWrap         = "timestamp wraparound"
	ReasonBackwards    = "non-monotonic timestamps beyond tolerance"
	ReasonBadClock     = "invalid clock"
)
