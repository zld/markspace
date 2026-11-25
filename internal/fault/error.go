// Package fault defines the single error structure shared by every
// assetcheck subsystem.
//
// A fault always answers four questions about a failure: which pipeline
// stage detected it, where in the input bytes it happened, what media time
// it belongs to, and why it happened. Keeping all four on one type lets the
// scanner, the validator and the streaming runner report failures in the
// same shape, and lets the reporter sort them deterministically.
package fault

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"example.com/assetcheck/internal/clock"
)

// Stage names the pipeline stage that produced a fault.
type Stage string

const (
	// StageScan covers container/box traversal and RTP packet parsing.
	StageScan Stage = "scan"
	// StageIndex covers index construction, verification and rebuilding.
	StageIndex Stage = "index"
	// StageCodec covers Opus/AAC packet duration mapping.
	StageCodec Stage = "codec"
	// StageValidate covers track, fragment and subtitle constraint checks.
	StageValidate Stage = "validate"
	// StageStream covers the streaming execution of a scanned index.
	StageStream Stage = "stream"
)

// NoOffset marks a fault that cannot be tied to an input byte position.
const NoOffset int64 = -1

// NoSample marks a fault that is not attributable to a single sample.
const NoSample int = -1

// Error is the shared, position-aware fault type. Offset is a byte position
// in the original input (always >= 0 when known); Time is a media position on
// the nanosecond clock (clock.Invalid when unknown); Sample is the zero-based
// sample index within its track (NoSample when not applicable).
type Error struct {
	Stage  Stage
	Code   string
	Offset int64
	Time   clock.Nanos
	Sample int
	Msg    string
	Cause  error
}

// New builds a fault without an underlying cause.
func New(stage Stage, code string, offset int64, at clock.Nanos, msg string) *Error {
	return &Error{Stage: stage, Code: code, Offset: offset, Time: at, Sample: NoSample, Msg: msg}
}

// Wrap builds a fault that carries an underlying cause.
func Wrap(stage Stage, code string, offset int64, at clock.Nanos, msg string, cause error) *Error {
	e := New(stage, code, offset, at, msg)
	e.Cause = cause
	return e
}

// WithSample attaches a sample index and returns the receiver.
func (e *Error) WithSample(index int) *Error {
	e.Sample = index
	return e
}

// Error renders every locating field in a fixed order so the textual form is
// stable across runs.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("stage=")
	b.WriteString(string(e.Stage))
	b.WriteString(" code=")
	b.WriteString(e.Code)
	if e.Offset >= 0 {
		fmt.Fprintf(&b, " offset=%d", e.Offset)
	} else {
		b.WriteString(" offset=unknown")
	}
	if e.Time != clock.Invalid {
		fmt.Fprintf(&b, " time=%dns", int64(e.Time))
	} else {
		b.WriteString(" time=unknown")
	}
	if e.Sample != NoSample {
		fmt.Fprintf(&b, " sample=%d", e.Sample)
	}
	b.WriteString(": ")
	b.WriteString(e.Msg)
	if e.Cause != nil {
		b.WriteString(": ")
		b.WriteString(e.Cause.Error())
	}
	return b.String()
}

// Unwrap exposes the cause to errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// From converts any error into a *Error. Faults pass through unchanged;
// foreign errors are annotated with the supplied stage and code.
func From(err error, stage Stage, code string) *Error {
	if err == nil {
		return nil
	}
	var f *Error
	if errors.As(err, &f) {
		return f
	}
	return Wrap(stage, code, NoOffset, clock.Invalid, "unclassified failure", err)
}

// Sort orders faults deterministically: byte offset first (unknown last),
// then media time (unknown last), stage, code, sample, message.
func Sort(list []*Error) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.Offset != b.Offset {
			if a.Offset < 0 {
				return false
			}
			if b.Offset < 0 {
				return true
			}
			return a.Offset < b.Offset
		}
		if a.Time != b.Time {
			if a.Time == clock.Invalid {
				return false
			}
			if b.Time == clock.Invalid {
				return true
			}
			return a.Time < b.Time
		}
		if a.Stage != b.Stage {
			return a.Stage < b.Stage
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		if a.Sample != b.Sample {
			return a.Sample < b.Sample
		}
		return a.Msg < b.Msg
	})
}

// Strings renders a fault list in sorted order for logs and reports.
func Strings(list []*Error) []string {
	Sort(list)
	out := make([]string, len(list))
	for i, e := range list {
		out[i] = e.Error()
	}
	return out
}
