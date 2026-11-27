// Package model defines the stable error and record vocabulary shared by every
// subsystem of the media rebuild kernel.
//
// Errors are values: a Code identifies a failure class independently of its
// message, so callers and tests can branch on it and manifests can persist it.
// Every error carries the source it came from plus a line or byte offset so a
// failed input item can be pointed at in the original material.
package model

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Code is a stable, machine-readable error classification.
type Code string

// Error codes. These strings are part of the manifest contract and must not be
// renamed without a manifest version bump.
const (
	CodeInvalidArgument Code = "invalid_argument"
	CodeOverflow        Code = "overflow"

	CodeSubtitleSyntax   Code = "subtitle_syntax"
	CodeSubtitleTiming   Code = "subtitle_timing"
	CodeSubtitleEncoding Code = "subtitle_encoding"

	CodeTimecodeSyntax    Code = "timecode_syntax"
	CodeTimecodeFrame     Code = "timecode_frame"
	CodeTimecodeDropFrame Code = "timecode_drop_frame"
	CodeTimecodeRate      Code = "timecode_rate"

	CodeRTPTruncated Code = "rtp_truncated"
	CodeRTPHeader    Code = "rtp_header"
	CodeRTPSequence  Code = "rtp_sequence"

	CodeMP4Container Code = "mp4_container"
	CodeMP4Track     Code = "mp4_track"
	CodeMP4Sample    Code = "mp4_sample"
	CodeMP4Truncated Code = "mp4_truncated"

	CodeTimelineSkew Code = "timeline_skew"

	CodeJournalCorrupt Code = "journal_corrupt"
	CodeTxnConflict    Code = "txn_conflict"
	CodeIO             Code = "io"
)

// Source names an input stream. Line is 1-based; Offset is a byte offset and
// is reported as -1 when unknown.
type Source struct {
	Name   string `json:"name"`
	Line   int    `json:"line,omitempty"`
	Offset int64  `json:"offset"`
}

// Error is the stable error model. It implements error and wraps an optional
// cause, so errors.Is/As keep working for sentinel values.
type Error struct {
	Code   Code   `json:"code"`
	Source Source `json:"source"`
	Detail string `json:"detail,omitempty"`
	Inner  error  `json:"-"`
}

// New builds an Error.
func New(code Code, src Source, detail string, inner error) *Error {
	if src.Offset == 0 && src.Line == 0 {
		src.Offset = -1
	}
	return &Error{Code: code, Source: src, Detail: detail, Inner: inner}
}

// Newf is New with printf-style detail.
func Newf(code Code, src Source, format string, args ...any) *Error {
	return New(code, src, fmt.Sprintf(format, args...), nil)
}

// AtLine returns a copy of the error anchored at line (1-based).
func (e *Error) AtLine(line int) *Error {
	c := *e
	c.Source.Line = line
	return &c
}

// AtOffset returns a copy of the error anchored at a byte offset.
func (e *Error) AtOffset(off int64) *Error {
	c := *e
	c.Source.Offset = off
	return &c
}

// WithSourceName returns a copy of the error naming its input source.
func (e *Error) WithSourceName(name string) *Error {
	c := *e
	c.Source.Name = name
	return &c
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	if e.Source.Name != "" {
		b.WriteString(" source=")
		b.WriteString(e.Source.Name)
	}
	if e.Source.Line > 0 {
		fmt.Fprintf(&b, " line=%d", e.Source.Line)
	}
	if e.Source.Offset >= 0 {
		fmt.Fprintf(&b, " offset=%d", e.Source.Offset)
	}
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	if e.Inner != nil {
		b.WriteString(" (")
		b.WriteString(e.Inner.Error())
		b.WriteString(")")
	}
	return b.String()
}

// Unwrap exposes the inner cause.
func (e *Error) Unwrap() error { return e.Inner }

// CodeOf returns the stable code of err, or CodeIO for unclassified errors.
func CodeOf(err error) Code {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeIO
}

// AsError returns err as *Error when it is one.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsCode reports whether err carries code.
func IsCode(err error, code Code) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// SortErrors orders errors deterministically by source, line, offset then code.
func SortErrors(errs []*Error) {
	sort.SliceStable(errs, func(i, j int) bool {
		a, b := errs[i], errs[j]
		if a.Source.Name != b.Source.Name {
			return a.Source.Name < b.Source.Name
		}
		if a.Source.Line != b.Source.Line {
			return a.Source.Line < b.Source.Line
		}
		if a.Source.Offset != b.Source.Offset {
			return a.Source.Offset < b.Source.Offset
		}
		return a.Code < b.Code
	})
}
