// Package fault defines the single error model shared by every archivecheck
// subsystem. Every failure carries the stage that produced it, the source it
// was produced from and the byte offset it points at, so a damaged segment can
// be traced back to an exact position in an immutable source.
package fault

import (
	"errors"
	"fmt"
	"strings"
)

// Stage names the processing stage that produced an error. The values double
// as checkpoint stage names, which is why they are plain strings.
type Stage string

const (
	StageSource     Stage = "source"
	StageSubtitle   Stage = "subtitle"
	StageTimecode   Stage = "timecode"
	StageMP4        Stage = "mp4"
	StageRTP        Stage = "rtp"
	StagePipeline   Stage = "pipeline"
	StageRecovery   Stage = "recovery"
	StageManifest   Stage = "manifest"
	StageDemo       Stage = "demo"
	StageCheckpoint Stage = "checkpoint"
)

// Kinds classify failures without tying callers to error text.
const (
	KindInvalid   = "invalid"
	KindTruncated = "truncated"
	KindOverflow  = "overflow"
	KindMissing   = "missing"
	KindConflict  = "conflict"
	KindCancelled = "cancelled"
	KindBoundary  = "path-boundary"
	KindDigest    = "digest-mismatch"
	KindLimit     = "limit-exceeded"
	KindCorrupt   = "corrupt"
)

// Err is an offset-carrying error. Offset and End are byte offsets into the
// named source; End equals Offset for failures that point at a single byte.
type Err struct {
	Stage  Stage
	Kind   string
	Source string
	Offset int64
	End    int64
	Detail string
	Err    error
}

func (e *Err) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s/%s", e.Stage, e.Kind)
	if e.Source != "" {
		fmt.Fprintf(&b, " source=%s", e.Source)
	}
	if e.End > e.Offset {
		fmt.Fprintf(&b, " offset=%d..%d", e.Offset, e.End)
	} else {
		fmt.Fprintf(&b, " offset=%d", e.Offset)
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, ": %s", e.Detail)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, " (%v)", e.Err)
	}
	return b.String()
}

func (e *Err) Unwrap() error { return e.Err }

// New builds an offset-carrying error without an underlying cause.
func New(stage Stage, kind, source string, offset int64, format string, args ...any) *Err {
	return &Err{Stage: stage, Kind: kind, Source: source, Offset: offset, End: offset, Detail: fmt.Sprintf(format, args...)}
}

// Wrap builds an offset-carrying error around an underlying cause.
func Wrap(err error, stage Stage, kind, source string, offset int64, format string, args ...any) *Err {
	return &Err{Stage: stage, Kind: kind, Source: source, Offset: offset, End: offset, Detail: fmt.Sprintf(format, args...), Err: err}
}

// Span sets the inclusive start and exclusive end of the damaged region.
func (e *Err) Span(start, end int64) *Err { e.Offset, e.End = start, end; return e }

// As extracts an *Err from err's chain. It returns nil when err carries no
// offset information.
func As(err error) *Err {
	var fe *Err
	if errors.As(err, &fe) {
		return fe
	}
	return nil
}

// OffsetOf reports the byte offset carried by err, or -1.
func OffsetOf(err error) int64 {
	if fe := As(err); fe != nil {
		return fe.Offset
	}
	return -1
}

// Is reports whether err's chain contains an *Err with the given stage and
// kind. A kind of "" matches any kind.
func Is(err error, stage Stage, kind string) bool {
	var fe *Err
	if errors.As(err, &fe) && fe.Stage == stage && (kind == "" || fe.Kind == kind) {
		return true
	}
	return false
}

// HasKind reports whether err's chain contains an *Err of the given kind,
// regardless of stage.
func HasKind(err error, kind string) bool {
	var fe *Err
	return errors.As(err, &fe) && fe.Kind == kind
}

// Join merges several failures into one while preserving every offset-bearing
// error in the chain, so a truncated file can report all damaged segments.
func Join(errs ...error) error {
	n := 0
	for _, e := range errs {
		if e != nil {
			n++
		}
	}
	switch n {
	case 0:
		return nil
	case 1:
		for _, e := range errs {
			if e != nil {
				return e
			}
		}
	}
	return errors.Join(errs...)
}

// Span is a damaged region of a source, in original byte offsets.
type Span struct {
	Start  int64  `json:"start"`
	End    int64  `json:"end"`
	Kind   string `json:"kind"`
	Detail string `json:"detail,omitempty"`
}

// Spans collects the spans carried by every *Err reachable from errs.
func Spans(errs ...error) []Span {
	var out []Span
	for _, err := range errs {
		collectSpans(err, &out)
	}
	return out
}

func collectSpans(err error, out *[]Span) {
	switch e := err.(type) {
	case nil:
		return
	case *Err:
		*out = append(*out, Span{Start: e.Offset, End: e.End, Kind: e.Kind, Detail: e.Detail})
		if e.Err != nil {
			collectSpans(e.Err, out)
		}
	case interface{ Unwrap() []error }:
		for _, u := range e.Unwrap() {
			collectSpans(u, out)
		}
	case interface{ Unwrap() error }:
		collectSpans(e.Unwrap(), out)
	}
}
