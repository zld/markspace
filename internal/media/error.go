package media

import (
	"errors"
	"fmt"
)

// Code is a stable, machine-readable error code. Codes are part of the
// output contract: they appear in summaries and must not change once
// published.
type Code string

const (
	// CodeIO covers local file read/write failures.
	CodeIO Code = "IO_ERROR"
	// CodeTruncatedBox reports an MP4 box whose declared size runs past the
	// end of the file, or a partially present box header.
	CodeTruncatedBox Code = "TRUNCATED_BOX"
	// CodeInvalidBox reports structurally invalid MP4 data that is not a
	// truncation (bad sizes, impossible counts).
	CodeInvalidBox Code = "INVALID_BOX"
	// CodeNoTrack reports a required track or sample table that is absent.
	CodeNoTrack Code = "NO_TRACK"
	// CodeMXFOutOfRange reports an MXF KLV chunk whose key or length claims
	// bytes beyond the file or parent batch item.
	CodeMXFOutOfRange Code = "MXF_KLV_OUT_OF_RANGE"
	// CodeMXFMalformed reports otherwise invalid MXF structure.
	CodeMXFMalformed Code = "MXF_MALFORMED"
	// CodeMETAConflict reports rotation/crop/colour descriptions that
	// disagree and were not resolved by an explicit field source.
	CodeMETAConflict Code = "META_CONFLICT"
	// CodeBadTimestamp reports timestamps that cannot be repaired under the
	// configured policy.
	CodeBadTimestamp Code = "BAD_TIMESTAMP"
	// CodeEventWindow reports an event window that cannot be placed in the
	// timeline.
	CodeEventWindow Code = "EVENT_WINDOW"
	// CodeNoKeyframe reports that no decodable entry at or before a target
	// time exists.
	CodeNoKeyframe Code = "NO_KEYFRAME"
	// CodeCheckpointMismatch reports that input or parameters changed since
	// the checkpoint was written.
	CodeCheckpointMismatch Code = "CHECKPOINT_MISMATCH"
	// CodeCanceled reports cooperative cancellation.
	CodeCanceled Code = "CANCELED"
	// CodeConfig reports invalid command parameters.
	CodeConfig Code = "CONFIG"
)

// Error is the single error model shared by parsing, repair, indexing and
// delivery. It carries a stable code plus everything needed to locate the
// failure: file, byte offset, track, and time range.
type Error struct {
	Code    Code
	Op      string
	File    string
	Offset  int64
	TrackID uint32
	HasTime bool
	From    Rational
	To      Rational
	Msg     string
	Err     error
}

func (e *Error) Error() string {
	s := string(e.Code)
	if e.Op != "" {
		s += ": " + e.Op
	}
	if e.File != "" {
		s += " file=" + e.File
	}
	if e.Offset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.TrackID != 0 {
		s += fmt.Sprintf(" track=%d", e.TrackID)
	}
	if e.HasTime {
		s += fmt.Sprintf(" range=[%s,%s)", e.From, e.To)
	}
	if e.Msg != "" {
		s += ": " + e.Msg
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// AsError returns the media.Error carried by err, if any.
func AsError(err error) (*Error, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// CodeOf returns the stable code for err. Unknown errors map to CodeIO only
// when they wrap an os error; otherwise CodeInvalidBox is used so unexpected
// failures are visible rather than silently reported as transport issues.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	if me, ok := AsError(err); ok {
		return me.Code
	}
	return CodeInvalidBox
}

// Wrap attaches the shared model to err without losing an existing media
// error. Offset is only recorded when >= 0.
func Wrap(err error, code Code, op string, offset int64) error {
	if err == nil {
		return nil
	}
	if me, ok := AsError(err); ok {
		if me.Op == "" {
			me.Op = op
		}
		return me
	}
	e := &Error{Code: code, Op: op, Offset: offset, Err: err}
	if offset < 0 {
		e.Offset = -1
	}
	return e
}

// New builds a media error. Offset may be -1 when unknown.
func New(code Code, op string, offset int64, msg string) *Error {
	return &Error{Code: code, Op: op, Offset: offset, Msg: msg}
}
