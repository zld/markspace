// Package fault defines the single structured error type used by every stage.
package fault

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable failure class.
type Code string

const (
	TruncatedBox Code = "TRUNCATED_BOX"
	InvalidBox   Code = "INVALID_BOX"
	SampleOffset Code = "SAMPLE_OFFSET"
	Index        Code = "INDEX_ERROR"
	Unsupported  Code = "UNSUPPORTED_FORMAT"
	EventParse   Code = "EVENT_PARSE"
	TimeGap      Code = "TIME_GAP"
	Render       Code = "RENDER_FAILED"
	Validation   Code = "VALIDATION_FAILED"
	IO           Code = "IO_ERROR"
	Journal      Code = "JOURNAL_ERROR"
	Usage        Code = "USAGE_ERROR"
	InjectedExit Code = "INJECTED_EXIT"
)

// Stage identifies the pipeline phase that produced an Error.
type Stage string

const (
	StageFixture  Stage = "fixture"
	StageScan     Stage = "scan"
	StageIndex    Stage = "index"
	StageEvent    Stage = "event"
	StageTimeline Stage = "timeline"
	StageRender   Stage = "render"
	StageValidate Stage = "validate"
	StageJournal  Stage = "journal"
	StagePipeline Stage = "pipeline"
)

// Error retains source identity, phase, first unreadable byte offset, and media tick.
// Offset is -1 when no input byte offset applies; Tick is -1 similarly.
type Error struct {
	Code     Code
	Stage    Stage
	Path     string
	Offset   int64
	Tick     int64
	ExitCode int
	Err      error
}

// New constructs a structured fault.
func New(code Code, stage Stage, path string, offset, tick int64, format string, args ...any) *Error {
	var inner error
	if format != "" {
		inner = fmt.Errorf(format, args...)
	}
	exit := 1
	if IsDataFailure(code) {
		exit = 2
	}
	if code == InjectedExit {
		exit = 75 // EX_TEMPFAIL, used by the crash-after-rename acceptance fixture.
	}
	return &Error{Code: code, Stage: stage, Path: path, Offset: offset, Tick: tick, ExitCode: exit, Err: inner}
}

// Wrap preserves an existing fault or attaches one common Error envelope.
func Wrap(err error, code Code, stage Stage, path string, offset, tick int64) *Error {
	if err == nil {
		return nil
	}
	var fe *Error
	if errors.As(err, &fe) {
		if fe.Stage == "" {
			fe.Stage = stage
		}
		if fe.Path == "" {
			fe.Path = path
		}
		if fe.Offset < 0 && offset >= 0 {
			fe.Offset = offset
		}
		if fe.Tick < 0 && tick >= 0 {
			fe.Tick = tick
		}
		return fe
	}
	return New(code, stage, path, offset, tick, "%v", err)
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s stage=%s", e.Code, e.Stage)
	if e.Path != "" {
		s += " source=" + e.Path
	}
	if e.Offset >= 0 {
		s += fmt.Sprintf(" offset=%d raw_offset=%d", e.Offset, e.Offset)
	}
	if e.Tick >= 0 {
		s += fmt.Sprintf(" tick=%d", e.Tick)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the underlying error.
func (e *Error) Unwrap() error { return e.Err }

// IsDataFailure reports codes that the terminal interface maps to exit status 2.
func IsDataFailure(code Code) bool {
	switch code {
	case TruncatedBox, InvalidBox, SampleOffset, Unsupported, EventParse, TimeGap, Validation:
		return true
	default:
		return false
	}
}

// From finds a *Error in err's chain.
func From(err error) (*Error, bool) {
	var fe *Error
	return fe, errors.As(err, &fe)
}

// ExitStatus returns the process exit status for an error.
func ExitStatus(err error) int {
	var fe *Error
	if errors.As(err, &fe) {
		if fe.ExitCode != 0 {
			return fe.ExitCode
		}
		if IsDataFailure(fe.Code) {
			return 2
		}
	}
	return 1
}
