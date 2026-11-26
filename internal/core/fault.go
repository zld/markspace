package core

import (
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a fault so that scanning and execution classify anomalies
// identically.
type Kind string

const (
	// KindManifest: the manifest (DASH MPD or HLS playlist) is malformed.
	KindManifest Kind = "manifest"
	// KindPath: a referenced path escapes the media root or is absolute.
	KindPath Kind = "path"
	// KindIO: local file read/write failed.
	KindIO Kind = "io"
	// KindSegmentTruncated: the file ends before the boxes it declares.
	KindSegmentTruncated Kind = "segment-truncated"
	// KindBoxParse: an fMP4 box could not be parsed or is structurally invalid.
	KindBoxParse Kind = "box-parse"
	// KindHashMismatch: the SHA-256 differs from the indexed value.
	KindHashMismatch Kind = "hash-mismatch"
	// KindTimeJump: a timestamp wrap, backwards step or discontinuity.
	KindTimeJump Kind = "time-jump"
	// KindNoKeyframe: no verified keyframe remains in the requested interval.
	KindNoKeyframe Kind = "no-keyframe"
	// KindUsage: command-line arguments are invalid.
	KindUsage Kind = "usage"
	// KindReport: the report could not be committed.
	KindReport Kind = "report"
)

// ExitCode maps a fault kind to the process exit status contract:
// 0 report completed, 2 invalid arguments or manifest, 3 local I/O or report
// commit failure. Anomalies that are recorded in the report do not change the
// exit code.
func ExitCode(k Kind) int {
	switch k {
	case KindUsage, KindManifest, KindPath:
		return 2
	case KindIO, KindReport:
		return 3
	}
	return 3
}

// Recorded reports whether a fault of this kind is expected to be recorded in
// the anomaly report rather than aborting the run.
func Recorded(k Kind) bool {
	switch k {
	case KindSegmentTruncated, KindBoxParse, KindHashMismatch, KindTimeJump, KindNoKeyframe:
		return true
	}
	return false
}

// Pos is a location in an input: a file (relative to the media root when the
// file is media), a 1-based line inside a text manifest and, when relevant, a
// byte offset inside a media file.
type Pos struct {
	// File is the path relative to the media root, or the absolute path of an
	// input that lives outside it (manifests the user named explicitly).
	File string
	// Line is 1-based; zero means "not line-oriented".
	Line int
	// Byte is a 0-based byte offset inside File; -1 means unknown.
	Byte int64
}

// String renders the position compactly.
func (p Pos) String() string {
	var b strings.Builder
	if p.File == "" {
		b.WriteString("<unknown>")
	} else {
		b.WriteString(p.File)
	}
	if p.Line > 0 {
		fmt.Fprintf(&b, ":%d", p.Line)
	}
	if p.Byte >= 0 {
		fmt.Fprintf(&b, "@%d", p.Byte)
	}
	return b.String()
}

// Valid reports whether the position names a file.
func (p Pos) Valid() bool { return p.File != "" }

// Fault is the single error type used across medtrace. It always carries the
// input location and a kind; Wrapped retains the underlying cause for
// debugging but is never required for classification.
type Fault struct {
	Kind    Kind
	Pos     Pos
	Track   Track
	Op      string
	Detail  string
	Wrapped error
}

// NewFault builds a fault with no wrapped cause.
func NewFault(kind Kind, pos Pos, op, detail string) *Fault {
	return &Fault{Kind: kind, Pos: pos, Op: op, Detail: detail}
}

// WrapFault promotes err to a *Fault, filling in any position or kind it
// lacks. If err is already a *Fault its own fields win.
func WrapFault(err error, kind Kind, pos Pos, op, detail string) *Fault {
	if err == nil {
		return nil
	}
	var f *Fault
	if errors.As(err, &f) {
		return f
	}
	return &Fault{Kind: kind, Pos: pos, Op: op, Detail: detail, Wrapped: err}
}

// Error implements error.
func (f *Fault) Error() string {
	var b strings.Builder
	if f.Op != "" {
		b.WriteString(f.Op)
		b.WriteString(": ")
	}
	if f.Kind != "" {
		b.WriteString(string(f.Kind))
		b.WriteString(": ")
	}
	if f.Pos.Valid() {
		b.WriteString(f.Pos.String())
		b.WriteString(": ")
	}
	if f.Track.Kind != "" || f.Track.ID != 0 || f.Track.Representation != "" {
		fmt.Fprintf(&b, "track %s: ", f.Track.Key())
	}
	b.WriteString(f.Detail)
	if f.Wrapped != nil {
		b.WriteString(": ")
		b.WriteString(f.Wrapped.Error())
	}
	return b.String()
}

// Unwrap exposes the wrapped cause for errors.Is/As.
func (f *Fault) Unwrap() error { return f.Wrapped }

// KindOf classifies any error. A non-Fault error is treated as local I/O,
// because every other failure path constructs a typed fault explicitly.
func KindOf(err error) Kind {
	if err == nil {
		return ""
	}
	var f *Fault
	if errors.As(err, &f) {
		return f.Kind
	}
	return KindIO
}

// PosOf extracts the source position from a fault, if it has one.
func PosOf(err error) Pos {
	var f *Fault
	if errors.As(err, &f) {
		return f.Pos
	}
	return Pos{Byte: -1}
}

// AsFault converts any error to a *Fault without losing information.
func AsFault(err error, fallbackKind Kind) *Fault {
	if err == nil {
		return nil
	}
	var f *Fault
	if errors.As(err, &f) {
		return f
	}
	return &Fault{Kind: fallbackKind, Detail: err.Error(), Wrapped: err}
}
