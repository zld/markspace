package continuity

import (
	"errors"
	"fmt"
	"strconv"
)

// Kind classifies a failure. The kind is stable output and drives the CLI exit
// code: usage errors exit 1, every input or boundary rejection exits 2.
type Kind string

const (
	KindUsage        Kind = "usage"
	KindParse        Kind = "parse"
	KindPathEscape   Kind = "path-escape"
	KindTruncated    Kind = "truncated"
	KindUnsupported  Kind = "unsupported"
	KindBadReference Kind = "bad-reference"
	KindMapping      Kind = "mapping"
	KindLate         Kind = "late"
	KindQueueLimit   Kind = "queue-limit"
	KindCheckpoint   Kind = "checkpoint"
	KindMux          Kind = "mux"
)

// Location identifies the first input position at which a failure was
// detected. At least one field is set for every input error.
type Location struct {
	File    string // manifest or segment file, relative to the fixture root
	Line    int    // 1-based manifest line (0 when unknown)
	Offset  int64  // byte offset in File (-1 when unknown)
	Segment string // segment name, if the location is inside a segment
	Sample  int    // 0-based sample index within the segment (-1 when unknown)
}

// String renders a stable, human-readable position: file:line for text
// manifests and file@offset for byte-oriented segment data.
func (l Location) String() string {
	if l.File == "" && l.Segment == "" {
		return "unknown"
	}
	name := l.File
	if name == "" {
		name = l.Segment
	}
	switch {
	case l.Line > 0:
		return fmt.Sprintf("%s:%d", name, l.Line)
	case l.Offset >= 0:
		if l.Sample >= 0 {
			return fmt.Sprintf("%s@%d#%d", name, l.Offset, l.Sample)
		}
		return fmt.Sprintf("%s@%d", name, l.Offset)
	case l.Sample >= 0:
		return fmt.Sprintf("%s#%d", name, l.Sample)
	default:
		return name
	}
}

// Empty reports whether the location carries no position information.
func (l Location) Empty() bool {
	return l.File == "" && l.Segment == "" && l.Line == 0 && l.Offset < 0 && l.Sample < 0
}

// Error is the uniform fault model shared by manifests, demuxing, mapping,
// timeline, queueing and checkpoints. It always carries a class and, when
// available, the input position that triggered it.
type Error struct {
	Kind Kind
	Loc  Location
	Msg  string
	Err  error
}

// Error implements error.
func (e *Error) Error() string {
	msg := e.Msg
	if msg == "" && e.Err != nil {
		msg = e.Err.Error()
	}
	if e.Loc.Empty() {
		return string(e.Kind) + ": " + msg
	}
	return string(e.Kind) + ": " + msg + " [" + e.Loc.String() + "]"
}

// Unwrap exposes the underlying cause.
func (e *Error) Unwrap() error { return e.Err }

// NewError builds a fault at loc.
func NewError(kind Kind, loc Location, format string, args ...any) *Error {
	return &Error{Kind: kind, Loc: loc, Msg: fmt.Sprintf(format, args...)}
}

// WrapError wraps err as kind at loc, preserving the original cause.
func WrapError(err error, kind Kind, loc Location, format string, args ...any) *Error {
	return &Error{Kind: kind, Loc: loc, Msg: fmt.Sprintf(format, args...), Err: err}
}

// Usagef builds a usage-class fault. These map to exit code 1.
func Usagef(format string, args ...any) *Error {
	return &Error{Kind: KindUsage, Msg: fmt.Sprintf(format, args...)}
}

// AsError extracts a fault from an arbitrary error chain.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// IsKind reports whether err (or any wrapped cause) has the given kind.
func IsKind(err error, kind Kind) bool {
	e, ok := AsError(err)
	return ok && e.Kind == kind
}

// LocationOf returns the first fault location in the chain, or an empty
// location when err carries none.
func LocationOf(err error) Location {
	if e, ok := AsError(err); ok {
		return e.Loc
	}
	return Location{Offset: -1, Sample: -1}
}

// FailureLocation renders the location of err as "none" if unknown.
func FailureLocation(err error) string {
	if err == nil {
		return "none"
	}
	loc := LocationOf(err)
	if loc.Empty() {
		return "none"
	}
	return loc.String()
}

// ExitCode maps an error to the CLI contract: 0 success, 1 usage, 2 input or
// boundary rejection (including corrupt data, path escapes, mapping and
// checkpoint conflicts).
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if IsKind(err, KindUsage) {
		return 1
	}
	return 2
}

// locWithFile clones loc with File and Segment set to name.
func locWithFile(name string, offset int64) Location {
	return Location{File: name, Segment: name, Offset: offset, Sample: -1}
}

// atLine builds a manifest location.
func atLine(name string, line int) Location {
	return Location{File: name, Line: line, Offset: -1, Sample: -1}
}

// atSegmentSample builds a per-sample location.
func atSegmentSample(segment, file string, offset int64, sample int) Location {
	return Location{File: file, Segment: segment, Offset: offset, Sample: sample}
}

func (k Kind) String() string { return string(k) }

// exitCodeName is used in diagnostics only.
func exitCodeName(code int) string {
	switch code {
	case 0:
		return "ok"
	case 1:
		return "usage"
	case 2:
		return "rejected"
	default:
		return strconv.Itoa(code)
	}
}
