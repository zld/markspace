// Package fault defines the shared structured error model used by every
// replay subsystem. Errors carry machine-readable context (stage, track,
// SSRC, sequence number, byte offset, version) so that terminal output and
// tests can assert on the exact failure location without ever exposing
// media payload bytes.
package fault

import (
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a failure. The CLI maps kinds to process exit codes.
type Kind int

const (
	// KindArgument marks an unusable command line.
	KindArgument Kind = iota + 1
	// KindInput marks malformed or inconsistent input data.
	KindInput
	// KindTruncated marks a packet whose declared size exceeds the bytes
	// actually available at a known offset.
	KindTruncated
	// KindClock marks a timestamp that regressed or overflowed.
	KindClock
	// KindGraph marks an invalid track dependency graph.
	KindGraph
	// KindMerge marks a merge conflict that prevents publication.
	KindMerge
	// KindManifest marks a manifest that failed verification.
	KindManifest
	// KindState marks an illegal checkpoint transition.
	KindState
)

func (k Kind) String() string {
	switch k {
	case KindArgument:
		return "argument"
	case KindInput:
		return "input"
	case KindTruncated:
		return "truncated"
	case KindClock:
		return "clock"
	case KindGraph:
		return "graph"
	case KindMerge:
		return "merge"
	case KindManifest:
		return "manifest"
	case KindState:
		return "state"
	default:
		return "unknown"
	}
}

// Error is the single error type crossing subsystem boundaries.
type Error struct {
	Kind       Kind
	Stage      string
	Track      string
	Version    string
	Reason     string
	SSRC       uint32
	Seq        uint64
	ByteOffset int64
	InputIndex int
	HasSSRC    bool
	HasSeq     bool
	HasOffset  bool
	HasIndex   bool
	Cause      error
}

// New starts a structured error with a fixed kind and human reason.
func New(kind Kind, reason string) *Error {
	return &Error{Kind: kind, Reason: reason}
}

// At records the pipeline stage that produced the error.
func (e *Error) At(stage string) *Error { e.Stage = stage; return e }

// WithTrack records the logical track ID.
func (e *Error) WithTrack(track string) *Error { e.Track = track; return e }

// WithVersion records the media asset version.
func (e *Error) WithVersion(version string) *Error { e.Version = version; return e }

// WithSSRC records the RTP synchronization source.
func (e *Error) WithSSRC(ssrc uint32) *Error {
	e.SSRC = ssrc
	e.HasSSRC = true
	return e
}

// WithSeq records an RTP extended sequence number.
func (e *Error) WithSeq(seq uint64) *Error {
	e.Seq = seq
	e.HasSeq = true
	return e
}

// WithOffset records a byte offset inside the offending input stream.
func (e *Error) WithOffset(offset int64) *Error {
	e.ByteOffset = offset
	e.HasOffset = true
	return e
}

// WithIndex records the zero-based index of the offending input record.
func (e *Error) WithIndex(index int) *Error {
	e.InputIndex = index
	e.HasIndex = true
	return e
}

// Wrap attaches an underlying cause. The cause text is part of the error
// chain (useful for library diagnostics) but never contains payload bytes.
func (e *Error) Wrap(err error) *Error { e.Cause = err; return e }

// Error renders only metadata and reason text. It intentionally has no
// access to packet payloads.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString("replay ")
	b.WriteString(e.Kind.String())
	b.WriteString(" error")
	if e.Stage != "" {
		b.WriteString(": stage=")
		b.WriteString(e.Stage)
	}
	if e.Track != "" {
		b.WriteString(" track=")
		b.WriteString(e.Track)
	}
	if e.Version != "" {
		b.WriteString(" version=")
		b.WriteString(e.Version)
	}
	if e.HasSSRC {
		fmt.Fprintf(&b, " ssrc=%08x", e.SSRC)
	}
	if e.HasSeq {
		fmt.Fprintf(&b, " seq=%d", e.Seq)
	}
	if e.HasIndex {
		fmt.Fprintf(&b, " input_index=%d", e.InputIndex)
	}
	if e.HasOffset {
		fmt.Fprintf(&b, " byte_offset=%d", e.ByteOffset)
	}
	if e.Reason != "" {
		b.WriteString(": ")
		b.WriteString(e.Reason)
	}
	if e.Cause != nil {
		b.WriteString(": ")
		b.WriteString(e.Cause.Error())
	}
	return b.String()
}

// Unwrap exposes the cause to errors.Is/As.
func (e *Error) Unwrap() error { return e.Cause }

// As extracts a structured error from an arbitrary error chain.
func As(err error) (*Error, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

// IsKind reports whether err is a structured error of the given kind.
func IsKind(err error, kind Kind) bool {
	fe, ok := As(err)
	return ok && fe.Kind == kind
}

// ExitCode maps an error to the documented process exit codes:
// 0 success, 1 input or merge failure, 2 argument failure.
// Argument failures take precedence anywhere in a joined error tree.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if hasKind(err, KindArgument) {
		return 2
	}
	return 1
}

func hasKind(err error, kind Kind) bool {
	if err == nil {
		return false
	}
	if fe, ok := err.(*Error); ok && fe.Kind == kind {
		return true
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, child := range multi.Unwrap() {
			if hasKind(child, kind) {
				return true
			}
		}
		return false
	}
	if child := errors.Unwrap(err); child != nil {
		return hasKind(child, kind)
	}
	return false
}
