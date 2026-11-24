package model

import (
	"errors"
	"fmt"
)

// ErrKind enumerates the stable failure categories emitted by the kernel.
// Every failure that reaches the report carries one of these kinds so that
// downstream tooling never has to parse human-readable strings.
type ErrKind string

const (
	KindManifestMissing  ErrKind = "manifest_missing"
	KindZeroTimescale    ErrKind = "zero_timescale"
	KindMissingDuration  ErrKind = "missing_duration"
	KindIndexOutOfRange  ErrKind = "index_out_of_range"
	KindCorruptBox       ErrKind = "corrupt_box"
	KindTruncatedFrame   ErrKind = "truncated_frame"
	KindTimestampFallback ErrKind = "timestamp_fallback"
	KindRTPWrapUnrecoverable ErrKind = "rtp_wrap_unrecoverable"
	KindUnregisteredParser ErrKind = "unregistered_parser"
	KindFieldOutOfRange  ErrKind = "field_out_of_range"
	KindMisaligned       ErrKind = "misaligned_representation"
	KindInternal         ErrKind = "internal_error"
)

// SourceLoc locates an error inside the input tree: manifest representation,
// playlist entry, segment file and byte/frame offset, whichever apply.
type SourceLoc struct {
	Manifest       string `json:"manifest"`
	Representation string `json:"representation"`
	Entry          int    `json:"entry"` // 0-based playlist/MPD entry; -1 when N/A
	Segment        string `json:"segment"`
	Offset          int64  `json:"offset"` // byte offset inside segment; -1 when N/A
	Frame           int    `json:"frame"`  // frame index inside segment; -1 when N/A
}

// SourceError is the only error type that crosses subsystem boundaries.
// It carries a stable kind and a fully qualified source location.
type SourceError struct {
	Kind ErrKind   `json:"kind"`
	Loc  SourceLoc `json:"loc"`
	Msg  string    `json:"msg"`
}

func (e *SourceError) Error() string {
	return fmt.Sprintf("%s: %s (%s)", e.Kind, e.Msg, formatLoc(e.Loc))
}

func formatLoc(l SourceLoc) string {
	s := l.Manifest
	if l.Representation != "" {
		s += "/" + l.Representation
	}
	if l.Entry >= 0 {
		s += fmt.Sprintf("#%d", l.Entry)
	}
	if l.Segment != "" {
		s += " " + l.Segment
	}
	if l.Offset >= 0 {
		s += fmt.Sprintf("@%d", l.Offset)
	}
	if l.Frame >= 0 {
		s += fmt.Sprintf("[frame %d]", l.Frame)
	}
	return s
}

// NewError builds a sourced error.
func NewError(kind ErrKind, loc SourceLoc, msg string) *SourceError {
	return &SourceError{Kind: kind, Loc: loc, Msg: msg}
}

// WrapError reclassifies a generic failure with a source location. Already
// sourced errors are returned untouched so their original location survives.
func WrapError(kind ErrKind, loc SourceLoc, err error) *SourceError {
	if err == nil {
		return nil
	}
	var se *SourceError
	if errors.As(err, &se) {
		return se
	}
	return NewError(kind, loc, err.Error())
}

// AsSource extracts the *SourceError from err, if present.
func AsSource(err error) (*SourceError, bool) {
	var se *SourceError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}
