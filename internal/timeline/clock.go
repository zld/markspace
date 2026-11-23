// Package timeline normalizes Matroska ticks to a single nanosecond base and
// aligns multiple bitrate representations onto shared segment boundaries.
package timeline

import (
	"errors"
	"fmt"
	"math"
)

// Clock converts Matroska timecode ticks to nanoseconds and enforces
// monotonicity per track.
type Clock struct {
	// Scale is nanoseconds per tick (Matroska TimecodeScale).
	Scale uint64
	// WrapLimit is the tick value at which the encoder's timecode wraps. Zero
	// disables wraparound detection. Matroska timestamps are signed 16-bit
	// relative to the cluster, so a limit is only used when the source feeds
	// an unsigned tick stream.
	WrapLimit int64
}

// NewClock validates and builds a clock. Scale must be positive.
func NewClock(scale uint64) (Clock, error) {
	if scale == 0 {
		return Clock{}, errors.New("timeline: timecode scale must be positive")
	}
	if scale > math.MaxInt64 {
		return Clock{}, errors.New("timeline: timecode scale out of range")
	}
	return Clock{Scale: scale}, nil
}

// ToNanos converts ticks to nanoseconds, reporting overflow.
func (c Clock) ToNanos(ticks int64) (int64, error) {
	if ticks < 0 {
		return 0, fmt.Errorf("timeline: negative tick %d", ticks)
	}
	if c.Scale != 0 && uint64(ticks) > math.MaxInt64/c.Scale {
		return 0, fmt.Errorf("timeline: tick %d overflows nanoseconds", ticks)
	}
	return ticks * int64(c.Scale), nil
}

// IssueKind classifies timeline anomalies that must not be silently accepted.
type IssueKind string

const (
	IssueTimestampWrap    IssueKind = "timestamp_wrap"
	IssueTimestampReverse IssueKind = "timestamp_reverse"
	IssueSparseTrack      IssueKind = "sparse_track"
	IssueDuplicateTime    IssueKind = "duplicate_timestamp"
)

// Issue is a per-track timeline anomaly with the source location that caused
// it.
type Issue struct {
	Kind    IssueKind `json:"kind"`
	Track   uint64    `json:"track"`
	Offset  int64     `json:"offset"`
	Tick    int64     `json:"tick"`
	Message string    `json:"message"`
}

func (i Issue) Error() string {
	return fmt.Sprintf("timeline %s on track %d at offset %d: %s", i.Kind, i.Track, i.Offset, i.Message)
}

// TrackClock tracks one track's monotonic state.
type TrackClock struct {
	Clock
	FrameNanos int64 // nominal frame duration, used for sparse detection
	last       int64
	seen       int64
	issues     []Issue
}

// NewTrackClock builds a per-track clock. frameNanos may be zero if unknown.
func NewTrackClock(scale uint64, frameNanos int64, offset int64) (*TrackClock, error) {
	c, err := NewClock(scale)
	if err != nil {
		return nil, err
	}
	if offset < 0 {
		return nil, errors.New("timeline: negative track offset")
	}
	return &TrackClock{Clock: c, FrameNanos: frameNanos}, nil
}

// Observe records a tick at a source offset and returns its nanoseconds. It
// reports the first anomaly rather than repairing it: callers decide whether to
// reject the segment.
func (t *TrackClock) Observe(tick int64, sourceOffset int64) (int64, error) {
	nanos, err := t.ToNanos(tick)
	if err != nil {
		return 0, Issue{Kind: IssueTimestampReverse, Tick: tick, Offset: sourceOffset, Message: err.Error()}
	}
	if t.seen > 0 {
		switch {
		case nanos == t.last:
			issue := Issue{Kind: IssueDuplicateTime, Tick: tick, Offset: sourceOffset, Message: "duplicate timestamp"}
			t.issues = append(t.issues, issue)
			return nanos, issue
		case nanos < t.last:
			kind := IssueTimestampReverse
			if t.Clock.WrapLimit > 0 && t.last-nanos > t.Clock.WrapLimit/2 {
				kind = IssueTimestampWrap
			}
			issue := Issue{Kind: kind, Tick: tick, Offset: sourceOffset, Message: fmt.Sprintf("timestamp %d ns follows %d ns", nanos, t.last)}
			t.issues = append(t.issues, issue)
			return nanos, issue
		case t.FrameNanos > 0 && nanos-t.last > 4*t.FrameNanos:
			issue := Issue{Kind: IssueSparseTrack, Tick: tick, Offset: sourceOffset, Message: fmt.Sprintf("gap of %d ns exceeds four frames", nanos-t.last)}
			t.issues = append(t.issues, issue)
		}
	}
	t.last = nanos
	t.seen++
	return nanos, nil
}

// Issues returns anomalies observed so far.
func (t *TrackClock) Issues() []Issue { return append([]Issue(nil), t.issues...) }

// Last returns the last accepted nanosecond timestamp.
func (t *TrackClock) Last() int64 { return t.last }

// Observed reports how many timestamps were accepted.
func (t *TrackClock) Observed() int64 { return t.seen }
