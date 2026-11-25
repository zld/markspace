package kernel

import (
	"errors"
	"fmt"
)

// ClockHz is the shared MPEG-TS PCR/PTS time base.
const ClockHz uint64 = 90_000

// TimeRange is a half-open [Start90k, End90k) interval on the shared clock.
type TimeRange struct {
	Start90k uint64 `json:"start_90k"`
	End90k   uint64 `json:"end_90k"`
}

// RangeMS constructs a range from millisecond endpoints.
func RangeMS(startMS, endMS int64) TimeRange {
	return TimeRange{Start90k: uint64(startMS * 90), End90k: uint64(endMS * 90)}
}

// Duration90k returns the non-negative duration in 90 kHz ticks.
func (r TimeRange) Duration90k() uint64 {
	if r.End90k <= r.Start90k {
		return 0
	}
	return r.End90k - r.Start90k
}

// DurationMS returns the duration truncated to milliseconds.
func (r TimeRange) DurationMS() int64 {
	return int64(r.Duration90k() / 90)
}

// StartMS converts the start to milliseconds.
func (r TimeRange) StartMS() int64 { return int64(r.Start90k / 90) }

// EndMS converts the end to milliseconds.
func (r TimeRange) EndMS() int64 { return int64(r.End90k / 90) }

// Contains reports whether t belongs to the half-open interval.
func (r TimeRange) Contains(t uint64) bool {
	return t >= r.Start90k && t < r.End90k
}

// Intersect returns the half-open intersection and whether it is non-empty.
func (r TimeRange) Intersect(other TimeRange) (TimeRange, bool) {
	start := max64(r.Start90k, other.Start90k)
	end := min64(r.End90k, other.End90k)
	if start >= end {
		return TimeRange{}, false
	}
	return TimeRange{Start90k: start, End90k: end}, true
}

func max64(a, b uint64) uint64 {
	if a > b {
		return a
	}
	return b
}

func min64(a, b uint64) uint64 {
	if a < b {
		return a
	}
	return b
}

// GapKind identifies a transport or presentation gap.
type GapKind string

const (
	GapRTPSequence   GapKind = "rtp_sequence_gap"
	GapRTPLate       GapKind = "rtp_late_outside_window"
	GapCCJump        GapKind = "continuity_count_jump"
	GapTruncatedTS   GapKind = "truncated_ts_packet"
	GapSyncLoss      GapKind = "ts_sync_loss"
	GapPSIVersion    GapKind = "psi_version_incomplete"
	GapSourceMutated GapKind = "source_digest_changed"
)

// GapEvent is a reviewable missing or inconsistent media interval.
type GapEvent struct {
	ID           string    `json:"id"`
	Kind         GapKind   `json:"kind"`
	PID          uint16    `json:"pid"`
	Time         TimeRange `json:"time"`
	FirstPacket  uint64    `json:"first_packet"`
	LastPacket   uint64    `json:"last_packet"`
	MissingCount int       `json:"missing_count,omitempty"`
	Detail       string    `json:"detail,omitempty"`
	Recoverable  bool      `json:"recoverable"`
}

// MediaError is the common error model used by every pipeline stage.
type MediaError struct {
	Stage       string    `json:"stage"`
	Code        string    `json:"code"`
	Message     string    `json:"message"`
	PID         uint16    `json:"pid"`
	Time        TimeRange `json:"time"`
	Packet      uint64    `json:"packet,omitempty"`
	Recoverable bool      `json:"recoverable"`
	Err         error     `json:"-"`
}

func (e *MediaError) Error() string {
	base := fmt.Sprintf("%s: %s", e.Stage, e.Message)
	if e.PID != 0 || e.Code != "" {
		base = fmt.Sprintf("%s [code=%s pid=0x%04x range=%d-%d]",
			base, e.Code, e.PID, e.Time.Start90k, e.Time.End90k)
	}
	if e.Err != nil {
		base += ": " + e.Err.Error()
	}
	return base
}

// Unwrap exposes the wrapped error.
func (e *MediaError) Unwrap() error { return e.Err }

// ExitCode maps the error to the terminal contract.
func (e *MediaError) ExitCode() int {
	if e.Recoverable {
		return 2
	}
	return 1
}

// ExitCoder is implemented by errors carrying a process exit status.
type ExitCoder interface {
	ExitCode() int
}

// CheckpointStop is returned when a requested --stop-after stage succeeds.
type CheckpointStop struct {
	Stage  string
	Packet uint64
}

func (e *CheckpointStop) Error() string {
	return fmt.Sprintf("recoverable checkpoint reached at stage=%s packet=%d", e.Stage, e.Packet)
}

// ExitCode implements ExitCoder with the recoverable-interrupt status.
func (*CheckpointStop) ExitCode() int { return 2 }

// AsMediaError converts an arbitrary error to *MediaError when possible.
func AsMediaError(err error) (*MediaError, bool) {
	var me *MediaError
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// ExitCodeOf returns the documented process exit code for an error.
func ExitCodeOf(err error) int {
	var coder ExitCoder
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return 1
}
