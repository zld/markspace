// Package mediaerr defines the unified error model shared by every
// processing stage. Each error carries the pipeline stage, track id, and
// when available the byte range and time range it applies to, so damage
// reports stay machine-readable end to end.
package mediaerr

import (
	"errors"
	"fmt"
)

// Processing stages, in pipeline order.
const (
	StageStream   = "stream"
	StageTimebase = "timebase"
	StageParse    = "parse"
	StageIndex    = "index"
	StageTxn      = "txn"
	StagePipeline = "pipeline"
)

// ByteRange is a half-open byte interval [Start, End) inside a track stream.
type ByteRange struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}

// Valid reports whether the range is well formed.
func (b ByteRange) Valid() bool { return b.Start >= 0 && b.End >= b.Start }

// OverlapsOrAdjacent reports whether two same-track ranges can be merged.
func (b ByteRange) OverlapsOrAdjacent(o ByteRange) bool {
	return b.Start <= o.End && o.Start <= b.End
}

// Merge returns the smallest range covering both inputs.
func (b ByteRange) Merge(o ByteRange) ByteRange {
	return ByteRange{Start: min(b.Start, o.Start), End: max(b.End, o.End)}
}

// TimeRange is the time span covered by a damage interval. Nil boundaries
// mean the side is unknown (for example a truncated tail with no PTS).
type TimeRange struct {
	FromTicks   int64  `json:"from_ticks"`
	ToTicks     int64  `json:"to_ticks"`
	TimeBaseNum uint64 `json:"timebase_num"`
	TimeBaseDen uint64 `json:"timebase_den"`
}

// MediaError is the structured error type used across the kernel.
type MediaError struct {
	Stage string     `json:"stage"`
	Track string     `json:"track,omitempty"`
	Code  string     `json:"code"`
	Msg   string     `json:"message"`
	Byte  *ByteRange `json:"byte_range,omitempty"`
	Time  *TimeRange `json:"time_range,omitempty"`
	Err   error      `json:"-"`
}

// New builds an error for a stage/track with a formatted message.
func New(stage, track, format string, args ...any) *MediaError {
	return &MediaError{Stage: stage, Track: track, Code: stage + ".error",
		Msg: fmt.Sprintf(format, args...)}
}

// NewCoded builds an error with an explicit machine-readable code.
func NewCoded(stage, track, code, format string, args ...any) *MediaError {
	return &MediaError{Stage: stage, Track: track, Code: code,
		Msg: fmt.Sprintf(format, args...)}
}

// Wrap annotates an existing error while keeping Unwrap usable.
func Wrap(err error, stage, track, format string, args ...any) *MediaError {
	return &MediaError{Stage: stage, Track: track, Code: stage + ".error",
		Msg: fmt.Sprintf(format, args...), Err: err}
}

// WithBytes attaches a byte range and returns the same error.
func (e *MediaError) WithBytes(r ByteRange) *MediaError { e.Byte = &r; return e }

// WithTime attaches a time range and returns the same error.
func (e *MediaError) WithTime(t TimeRange) *MediaError { e.Time = &t; return e }

// Error implements the error interface.
func (e *MediaError) Error() string {
	s := e.Stage
	if e.Track != "" {
		s += "/" + e.Track
	}
	s += ": " + e.Msg
	if e.Byte != nil {
		s += fmt.Sprintf(" [bytes %d-%d)", e.Byte.Start, e.Byte.End)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the wrapped cause for errors.Is/As.
func (e *MediaError) Unwrap() error { return e.Err }

// AsMedia extracts a *MediaError from any wrapped error chain.
func AsMedia(err error) (*MediaError, bool) {
	var me *MediaError
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// Mergeable reports whether two structured errors describe one continuous
// fault: same track and stage with touching or overlapping byte ranges.
func Mergeable(a, b *MediaError) bool {
	if a == nil || b == nil || a.Byte == nil || b.Byte == nil {
		return false
	}
	if a.Stage != b.Stage || a.Track != b.Track {
		return false
	}
	return a.Byte.OverlapsOrAdjacent(*b.Byte)
}

// Merge combines two mergeable errors, taking the wider byte and time range.
func Merge(a, b *MediaError) *MediaError {
	out := *a
	out.Byte = nil
	if a.Byte != nil || b.Byte != nil {
		r := ByteRange{}
		if a.Byte != nil {
			r = *a.Byte
		}
		if b.Byte != nil {
			if a.Byte == nil {
				r = *b.Byte
			} else {
				r = r.Merge(*b.Byte)
			}
		}
		out.Byte = &r
	}
	if b.Time != nil && out.Time == nil {
		t := *b.Time
		out.Time = &t
	} else if b.Time != nil && out.Time != nil {
		out.Time.ToTicks = max(out.Time.ToTicks, b.Time.ToTicks)
	}
	return &out
}

// Exit codes required by the terminal contract.
const (
	ExitOK              = 0
	ExitGeneric         = 1
	ExitInputOrIndex    = 2
	ExitCommitOrRecover = 3
)

// ExitCode maps an error to the terminal exit code. Nil means success.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	me, ok := AsMedia(err)
	if !ok {
		return ExitGeneric
	}
	switch me.Stage {
	case StageTxn:
		return ExitCommitOrRecover
	case StageStream, StageParse, StageIndex, StageTimebase:
		return ExitInputOrIndex
	default:
		return ExitGeneric
	}
}
