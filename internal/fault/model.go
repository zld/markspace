// Package fault holds the error model shared by every subsystem.
//
// The model separates four concerns so a single damaged input cannot be
// mistaken for a programming error or a transient I/O problem:
//
//	Kind       – what class of defect occurred (truncation, loss, ordering...)
//	Severity   – whether the defect prevents trusted delivery
//	Location   – byte or media offset where the defect was first observed
//	Cause      – underlying error, if any, for errors.Is/As
package fault

import (
	"errors"
	"fmt"
)

// Kind classifies a defect. Values are stable and appear in reports.
type Kind string

const (
	KindTruncated       Kind = "truncated"
	KindPartialFrame    Kind = "partial-sample-frame"
	KindBadHeader       Kind = "bad-header"
	KindBadChunk        Kind = "bad-chunk"
	KindUnsupported     Kind = "unsupported"
	KindOutOfRange      Kind = "out-of-range"
	KindDuplicatePacket Kind = "duplicate-packet"
	KindOutOfOrder      Kind = "out-of-order-packet"
	KindPacketLoss      Kind = "packet-loss"
	KindTimestampWrap   Kind = "timestamp-wrap"
	KindMissingKeyframe Kind = "missing-keyframe"
	KindUnreadable      Kind = "unreadable-sample"
	KindDigestMismatch  Kind = "digest-mismatch"
	KindStageStale      Kind = "stage-stale"
)

// Severity says how a defect affects delivery.
type Severity int

const (
	// Warn does not by itself invalidate output.
	Warn Severity = iota
	// Damaged marks a region that cannot be trusted but is localised.
	Damaged
	// Fatal prevents any further processing of the stream.
	Fatal
)

func (s Severity) String() string {
	switch s {
	case Warn:
		return "warn"
	case Damaged:
		return "damaged"
	case Fatal:
		return "fatal"
	default:
		return "unknown"
	}
}

// Location pins a defect to a byte offset and, when known, a media instant.
type Location struct {
	ByteOffset int64  `json:"byte_offset"`
	Tick       int64  `json:"tick"`
	HasTick    bool   `json:"has_tick"`
	Millis     int64  `json:"millis"`
	Label      string `json:"label,omitempty"`
}

// ByteAt returns a byte-only location.
func ByteAt(off int64) Location { return Location{ByteOffset: off} }

// Issue is a single defect observation.
type Issue struct {
	Kind     Kind     `json:"kind"`
	Severity Severity `json:"severity"`
	Where    Location `json:"where"`
	Message  string   `json:"message"`
	Cause    error    `json:"-"`
}

func (i Issue) Error() string {
	loc := fmt.Sprintf("byte %d", i.Where.ByteOffset)
	if i.Where.HasTick {
		loc += fmt.Sprintf(" tick %d (%dms)", i.Where.Tick, i.Where.Millis)
	}
	if i.Where.Label != "" {
		loc = i.Where.Label + " " + loc
	}
	msg := i.Message
	if msg == "" {
		msg = string(i.Kind)
	}
	return fmt.Sprintf("%s: %s: %s", i.Severity, msg, loc)
}

func (i Issue) Unwrap() error { return i.Cause }

// New builds an Issue.
func New(kind Kind, sev Severity, where Location, format string, args ...any) Issue {
	return Issue{Kind: kind, Severity: sev, Where: where, Message: fmt.Sprintf(format, args...)}
}

// Wrap builds an Issue that retains an underlying cause.
func Wrap(kind Kind, sev Severity, where Location, cause error, format string, args ...any) Issue {
	return Issue{Kind: kind, Severity: sev, Where: where, Message: fmt.Sprintf(format, args...), Cause: cause}
}

// IsKind reports whether err or any issue in its tree has the given kind.
func IsKind(err error, kind Kind) bool {
	var i Issue
	if errors.As(err, &i) && i.Kind == kind {
		return true
	}
	var list *List
	if errors.As(err, &list) {
		return list.HasKind(kind)
	}
	return false
}

// List is an ordered collection of issues that is itself an error.
type List struct {
	Issues []Issue `json:"issues"`
}

// Add appends issues.
func (l *List) Add(is ...Issue) { l.Issues = append(l.Issues, is...) }

// Len returns the number of issues.
func (l *List) Len() int { return len(l.Issues) }

// HasKind reports presence of a kind.
func (l *List) HasKind(kind Kind) bool {
	for _, i := range l.Issues {
		if i.Kind == kind {
			return true
		}
	}
	return false
}

// AtLeast reports whether any issue has severity >= s.
func (l *List) AtLeast(s Severity) bool {
	for _, i := range l.Issues {
		if i.Severity >= s {
			return true
		}
	}
	return false
}

// First returns the first issue of a kind, if any.
func (l *List) First(kind Kind) (Issue, bool) {
	for _, i := range l.Issues {
		if i.Kind == kind {
			return i, true
		}
	}
	return Issue{}, false
}

// FirstAtLeast returns the first issue with severity >= s.
func (l *List) FirstAtLeast(s Severity) (Issue, bool) {
	for _, i := range l.Issues {
		if i.Severity >= s {
			return i, true
		}
	}
	return Issue{}, false
}

func (l *List) Error() string {
	if len(l.Issues) == 0 {
		return "fault: no issues"
	}
	if len(l.Issues) == 1 {
		return l.Issues[0].Error()
	}
	first := l.Issues[0]
	return fmt.Sprintf("%s (and %d more issues)", first.Error(), len(l.Issues)-1)
}

func (l *List) Unwrap() []error {
	errs := make([]error, 0, len(l.Issues))
	for _, i := range l.Issues {
		errs = append(errs, i)
	}
	return errs
}
