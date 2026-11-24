package model

import (
	"errors"
	"fmt"
	"strings"
)

// Error codes are stable identifiers suitable for machine-readable output and
// tests. They never include volatile values.
const (
	CodeManifestSyntax      = "manifest.syntax"
	CodeManifestMissing     = "manifest.missing_field"
	CodeManifestNegative    = "manifest.negative_duration"
	CodeManifestInvalid     = "manifest.invalid_value"
	CodeManifestDuplicate   = "manifest.duplicate_track"
	CodeManifestEditRange   = "manifest.edit_out_of_range"
	CodeMP4Structure        = "mp4.structure"
	CodeMP4Truncated        = "mp4.truncated"
	CodeMP4ElstRange        = "mp4.edit_out_of_range"
	CodeRTPHeader           = "rtp.bad_header"
	CodeRTPLate             = "rtp.late_packet"
	CodeCheckpointMissing   = "checkpoint.missing"
	CodeCheckpointCorrupt   = "checkpoint.corrupt"
	CodeCheckpointPrefix    = "checkpoint.prefix_mismatch"
	CodeBufferOverflow      = "buffer.overflow"
	CodeTimelineLate        = "timeline.late_event"
	CodeVerificationFailure = "verify.failed"
)

// Loc describes where a problem was found. Field and ByteOffset are optional
// depending on the input type; HasByteOffset distinguishes offset zero from
// "no byte offset available".
type Loc struct {
	File          string
	TrackID       uint32
	TrackIndex    int
	Field         string
	ByteOffset    int64
	HasByteOffset bool
	PacketSeq     int64
	HasPacketSeq  bool
}

// Error is the unified diagnostic record shared by the manifest, MP4, RTP,
// buffer, timeline and checkpoint stages.
type Error struct {
	Code string
	Loc  Loc
	Msg  string
	Err  error
}

// NewError builds a structured error. Format arguments are formatted eagerly
// so the resulting text is stable and never contains pointer addresses.
func NewError(code string, loc Loc, format string, args ...interface{}) *Error {
	return &Error{Code: code, Loc: loc, Msg: fmt.Sprintf(format, args...)}
}

// Wrap attaches an underlying cause.
func (e *Error) Wrap(err error) *Error {
	e.Err = err
	return e
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Code)
	b.WriteString(": ")
	if e.Loc.File != "" {
		fmt.Fprintf(&b, "file=%s ", e.Loc.File)
	}
	if e.Loc.TrackID != 0 {
		fmt.Fprintf(&b, "track=%d ", e.Loc.TrackID)
	} else if e.Loc.TrackIndex >= 0 && e.Loc.Field != "" {
		fmt.Fprintf(&b, "track_index=%d ", e.Loc.TrackIndex)
	}
	if e.Loc.Field != "" {
		fmt.Fprintf(&b, "field=%s ", e.Loc.Field)
	}
	if e.Loc.HasByteOffset {
		fmt.Fprintf(&b, "offset=%d ", e.Loc.ByteOffset)
	}
	if e.Loc.HasPacketSeq {
		fmt.Fprintf(&b, "seq=%d ", e.Loc.PacketSeq)
	}
	b.WriteString(e.Msg)
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// ErrorList is a deterministic, ordered collection of diagnostics. The
// aggregation helpers let a stage continue after a recoverable finding while
// still returning a single error value.
type ErrorList []*Error

func (l ErrorList) Error() string {
	if len(l) == 0 {
		return "no errors"
	}
	parts := make([]string, 0, len(l))
	for _, e := range l {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}

func (l ErrorList) Unwrap() []error {
	out := make([]error, 0, len(l))
	for _, e := range l {
		out = append(out, e)
	}
	return out
}

// Add appends a structured error if it is non-nil.
func (l *ErrorList) Add(err *Error) {
	if err != nil {
		*l = append(*l, err)
	}
}

// Count returns the number of diagnostics.
func (l ErrorList) Count() int { return len(l) }

// As returns the first error in err's chain matching target.
func As(err error, target interface{}) bool { return errors.As(err, target) }
