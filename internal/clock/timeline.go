// Package clock defines the single time base shared by manifest parsing,
// RTP recovery, timeline synthesis and reporting.
//
// The internal unit is the tick: 1 tick == 1 millisecond. Every external
// timestamp (RTP clock at an arbitrary rate, manifest edit times expressed in
// milliseconds, non-zero start offsets) is converted to ticks before any
// cross-component comparison happens.
package clock

import (
	"errors"
	"fmt"
	"math"
)

// TicksPerSecond fixes the internal clock at 1 kHz, i.e. one tick per ms.
const TicksPerSecond int64 = 1000

// ErrorKind enumerates the stable, machine-readable failure categories shared
// by the manifest and RTP recovery stages.
type ErrorKind string

const (
	KindNegativeTime  ErrorKind = "negative_time"
	KindOverlap       ErrorKind = "overlapping_edit"
	KindOutOfRange    ErrorKind = "out_of_range_reference"
	KindOverflow      ErrorKind = "time_overflow"
	KindNonMonotonic  ErrorKind = "non_monotonic_timeline"
	KindChecksum      ErrorKind = "payload_checksum"
	KindLate          ErrorKind = "late_outside_window"
	KindTruncated     ErrorKind = "truncated_manifest"
	KindRenderFailure ErrorKind = "render_write_failure"
)

// MediaError carries enough location context (stage, track and tick position)
// for a failure to be replayed during post-incident review.
type MediaError struct {
	Kind   ErrorKind
	Stage  string
	Track  string
	AtTick int64
	Detail string
}

func (e *MediaError) Error() string {
	loc := e.Stage
	if e.Track != "" {
		loc += "/" + e.Track
	}
	return fmt.Sprintf("%s: %s at tick=%d: %s", loc, e.Kind, e.AtTick, e.Detail)
}

// NewError builds a MediaError. AtTick may be zero when the position is unknown.
func NewError(kind ErrorKind, stage, track string, atTick int64, detail string) *MediaError {
	return &MediaError{Kind: kind, Stage: stage, Track: track, AtTick: atTick, Detail: detail}
}

// As extracts a *MediaError from any wrapped error, if present.
func As(err error) (*MediaError, bool) {
	var me *MediaError
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// FromMillis converts a millisecond offset to ticks (identity at 1 kHz).
func FromMillis(ms int64) int64 { return ms }

// ToMillis converts ticks back to milliseconds.
func ToMillis(ticks int64) int64 { return ticks }

// FromSeconds converts whole seconds to ticks.
func FromSeconds(sec int64) (int64, error) {
	return CheckedMul(sec, TicksPerSecond)
}

// FromRTP maps an RTP timestamp running at clockRate Hz onto the tick base.
// Conversion uses integer division so identical inputs always produce
// identical ticks; the residual is below one tick for clockRate >= 1 kHz.
func FromRTP(rtpTS uint32, clockRate uint32) int64 {
	if clockRate == 0 {
		return 0
	}
	return int64(rtpTS) * TicksPerSecond / int64(clockRate)
}

// CheckedAdd returns an error instead of silently wrapping on int64 overflow.
func CheckedAdd(a, b int64) (int64, error) {
	if b > 0 && a > math.MaxInt64-b {
		return 0, NewError(KindOverflow, "clock", "", a, "addition overflow")
	}
	if b < 0 && a < math.MinInt64-b {
		return 0, NewError(KindOverflow, "clock", "", a, "addition underflow")
	}
	return a + b, nil
}

// CheckedMul returns an error instead of silently wrapping on int64 overflow.
func CheckedMul(a, b int64) (int64, error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	r := a * b
	if r/b != a {
		return 0, NewError(KindOverflow, "clock", "", a, "multiplication overflow")
	}
	return r, nil
}

// Range is a half-open interval [Start, End) on the tick timeline.
type Range struct {
	Start int64
	End   int64
}

// DurationMillis returns the interval length in milliseconds.
func (r Range) DurationMillis() int64 { return r.End - r.Start }

// Point is one positioned sample on the unified timeline.
type Point struct {
	At    int64
	Track string
	// Seq is the source RTP sequence number, kept for evidence correlation.
	Seq uint16
}

// Timeline stores per-track points and enforces strict monotonicity.
type Timeline struct {
	points []Point
	last   map[string]int64
}

// NewTimeline creates an empty timeline.
func NewTimeline() *Timeline {
	return &Timeline{last: map[string]int64{}}
}

// Append rejects negative times and per-track backwards/duplicate positions;
// time overflow is never swallowed.
func (t *Timeline) Append(p Point) error {
	if p.At < 0 {
		return NewError(KindNegativeTime, "timeline", p.Track, p.At, "point before epoch")
	}
	if prev, ok := t.last[p.Track]; ok {
		if p.At <= prev {
			return NewError(KindNonMonotonic, "timeline", p.Track, p.At,
				fmt.Sprintf("point not after previous tick %d", prev))
		}
	}
	t.points = append(t.points, p)
	t.last[p.Track] = p.At
	return nil
}

// Len returns the number of appended points.
func (t *Timeline) Len() int { return len(t.points) }

// Points returns the appended points in insertion order.
func (t *Timeline) Points() []Point {
	out := make([]Point, len(t.points))
	copy(out, t.points)
	return out
}

// CrossTrackError returns the largest absolute tick deviation between points
// occupying the same ordinal position on two tracks.
func CrossTrackError(a, b []Point) int64 {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	var worst int64
	for i := 0; i < n; i++ {
		d := a[i].At - b[i].At
		if d < 0 {
			d = -d
		}
		if d > worst {
			worst = d
		}
	}
	return worst
}
