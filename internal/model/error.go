package model

import (
	"errors"
	"fmt"
)

type Phase string

const (
	PhaseParse    Phase = "parse"
	PhaseMetadata Phase = "metadata"
	PhaseStream   Phase = "stream"
	PhaseProcess  Phase = "process"
	PhaseManifest Phase = "manifest"
	PhaseMigrate  Phase = "migrate"
)

type Code string

const (
	CodeOK                Code = "OK"
	CodeTruncated         Code = "TRUNCATED_SOURCE"
	CodeLengthOutOfRange  Code = "LENGTH_OUT_OF_RANGE"
	CodeUnsupportedCodec  Code = "UNSUPPORTED_CODEC"
	CodeInvalidMetadata   Code = "INVALID_METADATA"
	CodeCanceled          Code = "CANCELED"
	CodeMigrationStopped  Code = "MIGRATION_STOP_AFTER_PREPARE"
	CodeMigrationConflict Code = "MIGRATION_STATE_CONFLICT"
)

// Error is the common locatable failure used by every subsystem.
type Error struct {
	Code      Code       `json:"code"`
	Phase     Phase      `json:"phase"`
	Track     string     `json:"track,omitempty"`
	Offset    int64      `json:"offset,omitempty"`
	HasOffset bool       `json:"has_offset"`
	Message   string     `json:"message"`
	Intervals []Interval `json:"intervals,omitempty"`
	Err       error      `json:"-"`
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s/%s", e.Phase, e.Code)
	if e.Track != "" {
		s += " track=" + e.Track
	}
	if e.HasOffset {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Message != "" {
		s += ": " + e.Message
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

func NewError(code Code, phase Phase, track string, offset int64, format string, args ...any) *Error {
	return &Error{
		Code:      code,
		Phase:     phase,
		Track:     track,
		Offset:    offset,
		HasOffset: offset >= 0,
		Message:   fmt.Sprintf(format, args...),
	}
}

func (e *Error) Wrap(err error) *Error { e.Err = err; return e }

func (e *Error) WithIntervals(intervals ...Interval) *Error {
	e.Intervals = append(e.Intervals, intervals...)
	return e
}

// AsError finds the project error in err.
func AsError(err error) (*Error, bool) {
	var pe *Error
	if errors.As(err, &pe) {
		return pe, true
	}
	return nil, false
}

// InputExitCode maps malformed/unsupported input to the required terminal code.
func InputExitCode(err error) int {
	if err == nil {
		return 0
	}
	var pe *Error
	if errors.As(err, &pe) {
		switch pe.Code {
		case CodeMigrationStopped:
			return 3
		case CodeTruncated, CodeLengthOutOfRange, CodeUnsupportedCodec, CodeInvalidMetadata:
			return 2
		case CodeMigrationConflict:
			return 4
		default:
			return 1
		}
	}
	if errors.Is(err, ErrMigrationStopped) {
		return 3
	}
	return 1
}

var ErrMigrationStopped = errors.New("migration interrupted after prepare")

// Exception is a non-fatal anomaly retained for the terminal audit report.
type Exception struct {
	Kind       string    `json:"kind"`
	Phase      Phase     `json:"phase"`
	Track      string    `json:"track,omitempty"`
	Offset     int64     `json:"offset,omitempty"`
	HasOffset  bool      `json:"has_offset"`
	Interval   *Interval `json:"interval,omitempty"`
	Message    string    `json:"message"`
	Resolution string    `json:"resolution"`
}
