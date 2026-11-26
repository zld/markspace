package media

import (
	"errors"
	"fmt"
)

// Stage identifies the subsystem that produced a unified Error.
type Stage string

const (
	StageEBML       Stage = "ebml"
	StageTimeline   Stage = "timeline"
	StageCue        Stage = "cue"
	StageManifest   Stage = "manifest"
	StageAudio      Stage = "audio"
	StageColor      Stage = "color"
	StagePlanner    Stage = "planner"
	StageCheckpoint Stage = "checkpoint"
	StagePipeline   Stage = "pipeline"
)

// Sentinel failures let the CLI and callers classify outcomes without
// matching human-readable strings.
var (
	ErrInvalidVINT       = errors.New("invalid VINT")
	ErrTruncatedHeader   = errors.New("truncated EBML header")
	ErrTruncatedBlock    = errors.New("truncated block")
	ErrLengthOutOfBounds = errors.New("element length exceeds legal boundary")
	ErrIllegalBoundary   = errors.New("unknown-length element reached an illegal boundary")
	ErrMemoryBudget      = errors.New("memory budget exceeded")
	ErrTimeOverflow      = errors.New("timestamp overflow")
	ErrUnsortedEvents    = errors.New("events are not orderable")

	ErrUnknownLayout      = errors.New("unknown audio channel layout")
	ErrFieldConflict      = errors.New("manifest field conflict")
	ErrColorConflict      = errors.New("color metadata conflict at same priority")
	ErrUnsupportedVersion = errors.New("unsupported manifest version")
	ErrMigration          = errors.New("manifest migration failed")

	ErrCoverage   = errors.New("coverage below minimum")
	ErrAVSkew     = errors.New("audio/video skew above maximum")
	ErrInfeasible = errors.New("no conversion profile satisfies bitrate and budget")
	ErrBitrateCap = errors.New("target bitrate above cap")
	ErrBudget     = errors.New("processing budget exceeded")

	ErrCheckpointBinding = errors.New("checkpoint binding mismatch")
	ErrInputChanged      = errors.New("input prefix checksum changed")
	ErrConfigChanged     = errors.New("configuration checksum changed")
	ErrManifestChanged   = errors.New("manifest version/checksum changed")
)

// Error is the common structured error used by every subsystem.
//
// Offset is the absolute byte offset in the primary local input. Track is -1
// when an error is not associated with a particular track. Path is an EBML
// element path when available.
type Error struct {
	Stage  Stage  `json:"stage"`
	Offset int64  `json:"offset"`
	Track  int    `json:"track"`
	Path   string `json:"path,omitempty"`
	Kind   string `json:"kind,omitempty"`
	Err    error  `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	msg := fmt.Sprintf("%s error at offset=%d track=%d", e.Stage, e.Offset, e.Track)
	if e.Path != "" {
		msg += " path=" + e.Path
	}
	if e.Kind != "" {
		msg += " kind=" + e.Kind
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() []error {
	if e == nil || e.Err == nil {
		return nil
	}
	return []error{e.Err}
}

// NewError constructs a structured error. A negative offset means unknown.
func NewError(stage Stage, offset int64, path string, err error) *Error {
	return &Error{Stage: stage, Offset: offset, Track: -1, Path: path, Err: err}
}

// WithTrack associates an error with a track number.
func (e *Error) WithTrack(track uint32) *Error {
	if e == nil {
		return nil
	}
	e.Track = int(track)
	return e
}

// WithKind adds a machine-readable defect kind.
func (e *Error) WithKind(kind string) *Error {
	if e == nil {
		return nil
	}
	e.Kind = kind
	return e
}

// AsError finds a structured Error in err.
func AsError(err error) (*Error, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// IsInputCorruption reports whether err is a malformed local-input failure.
func IsInputCorruption(err error) bool {
	return errors.Is(err, ErrInvalidVINT) ||
		errors.Is(err, ErrTruncatedHeader) ||
		errors.Is(err, ErrTruncatedBlock) ||
		errors.Is(err, ErrLengthOutOfBounds) ||
		errors.Is(err, ErrIllegalBoundary) ||
		errors.Is(err, ErrMemoryBudget)
}

// IsConstraintFailure reports an unsatisfiable quality/budget constraint.
func IsConstraintFailure(err error) bool {
	return errors.Is(err, ErrCoverage) ||
		errors.Is(err, ErrAVSkew) ||
		errors.Is(err, ErrInfeasible) ||
		errors.Is(err, ErrBitrateCap) ||
		errors.Is(err, ErrBudget)
}

// IsConflict reports a resume/checkpoint binding conflict.
func IsConflict(err error) bool {
	return errors.Is(err, ErrCheckpointBinding) ||
		errors.Is(err, ErrInputChanged) ||
		errors.Is(err, ErrConfigChanged) ||
		errors.Is(err, ErrManifestChanged)
}
