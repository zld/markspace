// Package fault defines the single error type carried across the pipeline.
// Every failure names the input path, the raw byte offset within that path,
// the affected track, the media time and whether the defect is recoverable.
package fault

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"liveregress/internal/clock"
)

// Kind classifies a fault so callers can decide between continue, isolate and
// abort without string matching.
type Kind string

const (
	KindPath        Kind = "path"        // path escapes the input root or is unreadable
	KindFormat      Kind = "format"      // container cannot be parsed at all
	KindTruncate    Kind = "truncate"    // input ends inside a declared unit
	KindCorrupt     Kind = "corrupt"     // bytes are present but inconsistent
	KindMissing     Kind = "missing"     // declared track/box/index is absent
	KindMapping     Kind = "mapping"     // explicit track/channel mapping is invalid
	KindUnsupported Kind = "unsupported" // codec/feature deliberately not supported
	KindGap         Kind = "gap"         // timeline discontinuity
	KindIndex       Kind = "index"       // keyframe index disagrees with media
	KindVerify      Kind = "verify"      // post-remux content verification failed
	KindLimit       Kind = "limit"       // resource limit (segment size, depth)
)

// Error is the pipeline-wide error. It is comparable through errors.Is on the
// sentinel values below and unwraps to the original cause.
type Error struct {
	Kind        Kind
	Path        string
	Offset      int64
	Track       uint32
	TrackName   string
	Time        clock.Time
	Recoverable bool
	Layer       string // affected representation layer, e.g. "v0" or "a1"
	Detail      string
	Cause       error
}

// Error implements error with a stable, greppable single-line form.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Kind))
	if e.Path != "" {
		fmt.Fprintf(&b, " path=%s", e.Path)
	}
	if e.Offset >= 0 {
		fmt.Fprintf(&b, " offset=%d", e.Offset)
	}
	if e.Track != 0 || e.TrackName != "" {
		fmt.Fprintf(&b, " track=%d", e.Track)
		if e.TrackName != "" {
			fmt.Fprintf(&b, "(%s)", e.TrackName)
		}
	}
	if e.Time.Valid() {
		fmt.Fprintf(&b, " time=%s", e.Time)
	}
	if e.Layer != "" {
		fmt.Fprintf(&b, " layer=%s", e.Layer)
	}
	fmt.Fprintf(&b, " recoverable=%t", e.Recoverable)
	if e.Detail != "" {
		fmt.Fprintf(&b, " detail=%q", e.Detail)
	}
	if e.Cause != nil {
		fmt.Fprintf(&b, " cause=%q", e.Cause.Error())
	}
	return b.String()
}

// Unwrap exposes the cause for errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Cause }

// Is matches the sentinel of the same kind, so callers can do
// errors.Is(err, fault.ErrTruncated) without inspecting fields.
func (e *Error) Is(target error) bool {
	s, ok := target.(*sentinel)
	return ok && s.kind == e.Kind
}

type sentinel struct{ kind Kind }

func (s *sentinel) Error() string { return "fault: " + string(s.kind) }

// Sentinels for errors.Is.
var (
	ErrPath        = &sentinel{KindPath}
	ErrFormat      = &sentinel{KindFormat}
	ErrTruncated   = &sentinel{KindTruncate}
	ErrCorrupt     = &sentinel{KindCorrupt}
	ErrMissing     = &sentinel{KindMissing}
	ErrMapping     = &sentinel{KindMapping}
	ErrUnsupported = &sentinel{KindUnsupported}
	ErrGap         = &sentinel{KindGap}
	ErrIndex       = &sentinel{KindIndex}
	ErrVerify      = &sentinel{KindVerify}
	ErrLimit       = &sentinel{KindLimit}
)

// New builds an Error with a negative offset meaning "unknown". The offset the
// caller passes is the raw byte offset in the path, never a normalised one.
func New(kind Kind, path string, offset int64, detail string) *Error {
	return &Error{Kind: kind, Path: path, Offset: offset, Detail: detail}
}

// Wrap adds a cause to an Error built by New.
func Wrap(kind Kind, path string, offset int64, detail string, cause error) *Error {
	return &Error{Kind: kind, Path: path, Offset: offset, Detail: detail, Cause: cause}
}

// WithTrack attaches explicit track identity.
func (e *Error) WithTrack(id uint32, name string) *Error {
	e.Track, e.TrackName = id, name
	return e
}

// AtTime attaches the media time at which the fault was observed.
func (e *Error) AtTime(t clock.Time) *Error {
	e.Time = t
	return e
}

// InLayer attaches the representation layer.
func (e *Error) InLayer(layer string) *Error {
	e.Layer = layer
	return e
}

// Recover marks whether a complete prefix can still be committed.
func (e *Error) Recover(yes bool) *Error {
	e.Recoverable = yes
	return e
}

// As extracts the pipeline error if present.
func As(err error) (*Error, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

// Code maps a fault to the process exit code: 0 success, 2 recoverable defect,
// 1 invalid mapping or untrustworthy parse. Unknown errors are fatal (1).
func Code(err error) int {
	if err == nil {
		return 0
	}
	fe, ok := As(err)
	if !ok {
		return 1
	}
	switch fe.Kind {
	case KindGap, KindTruncate, KindCorrupt, KindIndex:
		if fe.Recoverable {
			return 2
		}
		return 1
	default:
		return 1
	}
}

// Summary aggregates several faults into a deterministic, sorted report.
type Summary struct {
	Faults []*Error
}

// Add appends a fault, ignoring nil.
func (s *Summary) Add(err error) {
	if err == nil {
		return
	}
	if fe, ok := As(err); ok {
		s.Faults = append(s.Faults, fe)
		return
	}
	s.Faults = append(s.Faults, Wrap(KindFormat, "", -1, err.Error(), err))
}

// Recoverable reports whether every collected fault is recoverable. An empty
// summary is recoverable (exit 0).
func (s *Summary) Recoverable() bool {
	for _, f := range s.Faults {
		if !f.Recoverable {
			return false
		}
	}
	return true
}

// ExitCode is 0 with no faults, 2 when all faults are recoverable, else 1.
func (s *Summary) ExitCode() int {
	if len(s.Faults) == 0 {
		return 0
	}
	if s.Recoverable() {
		return 2
	}
	return 1
}

// Sorted returns faults ordered by path, offset, track, then kind so output is
// stable across runs.
func (s *Summary) Sorted() []*Error {
	out := append([]*Error(nil), s.Faults...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Offset != b.Offset {
			return a.Offset < b.Offset
		}
		if a.Track != b.Track {
			return a.Track < b.Track
		}
		return a.Kind < b.Kind
	})
	return out
}
