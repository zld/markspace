package model

import (
	"errors"
	"fmt"
)

// Stage names the pipeline subsystem that produced an error. Every failure
// carried across the pipeline keeps the stage it originated in.
type Stage string

const (
	StageManifest Stage = "manifest"
	StageParse    Stage = "parse"
	StageMP4Scan  Stage = "mp4_scan"
	StageIndex    Stage = "mp4_index"
	StageRTP      Stage = "rtp_window"
	StageTimeline Stage = "timeline"
	StageMerge    Stage = "merge"
	StagePipeline Stage = "pipeline"
	StageReport   Stage = "report"
)

// Kind classifies an error for exit-code and report purposes.
type Kind int

const (
	// KindInternal is an unexpected failure: exit code 1.
	KindInternal Kind = iota
	// KindCorrupt means the input media or index is malformed or truncated.
	KindCorrupt
	// KindConflict means versions disagree and policy did not resolve it.
	KindConflict
	// KindInvalid means caller input (flags, manifest, config) is unusable.
	KindInvalid
	// KindCancelled means the context was cancelled before completion.
	KindCancelled
	// KindUnrecoverable means a loss or damage exceeded what the configured
	// recovery window/limits can repair.
	KindUnrecoverable
)

func (k Kind) String() string {
	switch k {
	case KindCorrupt:
		return "corrupt"
	case KindConflict:
		return "conflict"
	case KindInvalid:
		return "invalid_input"
	case KindCancelled:
		return "cancelled"
	case KindUnrecoverable:
		return "unrecoverable"
	default:
		return "internal"
	}
}

// ExitCode maps a kind to the process exit status contract:
// 2 for damaged input or unresolved conflict, 1 for everything else.
func (k Kind) ExitCode() int {
	switch k {
	case KindCorrupt, KindConflict, KindInvalid, KindUnrecoverable:
		return 2
	default:
		return 1
	}
}

// Error is the single error type that flows through every stage. It always
// identifies the asset, the track (when known), the stage, and the byte or
// sample offset at which the failure was detected, so a corrupt sample can be
// reported without any partial result being committed.
type Error struct {
	Kind    Kind
	Stage   Stage
	Asset   string
	Version string
	Track   string
	Offset  int64
	HasOff  bool
	Op      string
	Cause   error
}

func (e *Error) Error() string {
	loc := e.Asset
	if e.Version != "" {
		loc += "@" + e.Version
	}
	if loc == "" {
		loc = "<unknown>"
	}
	msg := fmt.Sprintf("%s error: asset=%s stage=%s", e.Kind, loc, e.Stage)
	if e.Track != "" {
		msg += " track=" + e.Track
	}
	if e.HasOff {
		msg += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Op != "" {
		msg += " op=" + e.Op
	}
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

// Unwrap exposes the underlying cause so errors.Is/As work.
func (e *Error) Unwrap() error { return e.Cause }

// ExitCode returns the process status for this error.
func (e *Error) ExitCode() int { return e.Kind.ExitCode() }

// New builds a stage-scoped error.
func New(kind Kind, stage Stage, asset, op string, cause error) *Error {
	return &Error{Kind: kind, Stage: stage, Asset: asset, Op: op, Cause: cause}
}

// WithTrack returns a copy annotated with a track identifier.
func (e *Error) WithTrack(track string) *Error {
	c := *e
	c.Track = track
	return &c
}

// WithVersion returns a copy annotated with a manifest version.
func (e *Error) WithVersion(version string) *Error {
	c := *e
	c.Version = version
	return &c
}

// WithOffset returns a copy annotated with a byte or sample offset.
func (e *Error) WithOffset(off int64) *Error {
	c := *e
	c.Offset = off
	c.HasOff = true
	return &c
}

// WithKind returns a copy reclassified into another kind.
func (e *Error) WithKind(kind Kind) *Error {
	c := *e
	c.Kind = kind
	return &c
}

// Wrap attaches stage context while preserving an existing *Error untouched;
// only errors that are not already structured get annotated.
func Wrap(err error, kind Kind, stage Stage, asset, op string) error {
	if err == nil {
		return nil
	}
	var me *Error
	if errors.As(err, &me) {
		return err
	}
	return New(kind, stage, asset, op, err)
}

// AsError extracts a structured error from err, wrapping plain errors in one
// labelled internal error when necessary.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	var me *Error
	if errors.As(err, &me) {
		return me
	}
	return New(KindInternal, StagePipeline, "", "", err)
}

// ExitCodeOf returns the contract exit code for an arbitrary error.
func ExitCodeOf(err error) int {
	if err == nil {
		return 0
	}
	return AsError(err).ExitCode()
}

// MultiError aggregates independently detected problems (missing items,
// conflicts) while preserving order for deterministic reports.
type MultiError struct {
	Kind   Kind
	Label  string
	Errors []*Error
}

func (m *MultiError) Error() string {
	return fmt.Sprintf("%s: %s (%d problems)", m.Kind, m.Label, len(m.Errors))
}

func (m *MultiError) Unwrap() []error {
	out := make([]error, len(m.Errors))
	for i, e := range m.Errors {
		out[i] = e
	}
	return out
}

// ExitCode follows the contained errors.
func (m *MultiError) ExitCode() int {
	if len(m.Errors) == 0 {
		return m.Kind.ExitCode()
	}
	return m.Errors[0].ExitCode()
}
