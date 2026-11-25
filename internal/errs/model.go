// Package errs defines the single structured error model carried through
// every subsystem. An Error always identifies the input source and, where
// the parser can determine it, the raw byte offset that triggered it, plus
// the track and segment sequence numbers when they are known.
package errs

import (
	"errors"
	"fmt"
	"strings"
)

// Kind classifies a failure so callers can decide whether a chunk may be
// retried or must be rejected.
type Kind string

const (
	// KindManifest covers malformed DASH MPD or HLS playlist text/tags.
	KindManifest Kind = "manifest"
	// KindContainer covers truncated or structurally invalid MP4 boxes.
	KindContainer Kind = "container"
	// KindTrack covers timescale, time-base or sample-range conflicts.
	KindTrack Kind = "track"
	// KindSegment covers duplicate, missing or out-of-range segment refs.
	KindSegment Kind = "segment"
	// KindTimeline covers the >20ms cross-representation misalignment.
	KindTimeline Kind = "timeline"
	// KindRTP covers RTP packet boundary or timestamp failures.
	KindRTP Kind = "rtp"
	// KindIndex covers an unreadable or incomplete keyframe index.
	KindIndex Kind = "index"
	// KindCheckpoint covers checkpoint/index persistence problems.
	KindCheckpoint Kind = "checkpoint"
	// KindArgs covers invalid CLI arguments or index structure errors.
	KindArgs Kind = "args"
	// KindIO covers local file read failures and rejected out-of-range reads.
	KindIO Kind = "io"
)

// Unknown is the sentinel for an offset or sequence number that cannot be
// determined. It must never be substituted by a guess.
const Unknown int64 = -1

// Error is the structured error type used everywhere in mediascan.
type Error struct {
	Kind    Kind   `json:"kind"`
	Source  string `json:"source"`
	Offset  int64  `json:"offset"`
	TrackID uint32 `json:"track_id,omitempty"`
	Segment int64  `json:"segment,omitempty"`
	Message string `json:"message"`
	Cause   string `json:"cause,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Kind))
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.Source != "" {
		fmt.Fprintf(&b, " [source=%s", e.Source)
		if e.Offset != Unknown {
			fmt.Fprintf(&b, " offset=%d", e.Offset)
		}
		if e.TrackID != 0 {
			fmt.Fprintf(&b, " track=%d", e.TrackID)
		}
		if e.Segment != Unknown {
			fmt.Fprintf(&b, " segment=%d", e.Segment)
		}
		b.WriteByte(']')
	}
	if e.Cause != "" {
		fmt.Fprintf(&b, ": %s", e.Cause)
	}
	return b.String()
}

// New builds an Error. Pass Unknown for offset/segment when not known.
func New(kind Kind, source string, offset, segment int64, trackID uint32, format string, args ...any) *Error {
	return &Error{
		Kind:    kind,
		Source:  source,
		Offset:  offset,
		TrackID: trackID,
		Segment: segment,
		Message: fmt.Sprintf(format, args...),
	}
}

// Wrap attaches a parser-level cause while preserving the structured fields.
func Wrap(e *Error, cause error) *Error {
	if e == nil {
		return nil
	}
	cp := *e
	if cause != nil {
		cp.Cause = cause.Error()
	}
	return &cp
}

// AsError extracts a *Error from any error value.
func AsError(err error) (*Error, bool) {
	var se *Error
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}

// Collection aggregates structured errors from a whole batch. A batch with
// collected errors is still allowed to finish; committed checkpoints remain
// valid and only the failing objects are kept out of the index.
type Collection struct {
	items []*Error
}

// Add records an error. Nil errors are ignored.
func (c *Collection) Add(err error) {
	if err == nil {
		return
	}
	if se, ok := AsError(err); ok {
		c.items = append(c.items, se)
		return
	}
	c.items = append(c.items, &Error{
		Kind: KindIO, Source: "", Offset: Unknown, Segment: Unknown,
		Message: err.Error(),
	})
}

// All returns the recorded errors in insertion order.
func (c *Collection) All() []*Error { return c.items }

// Len reports how many errors were collected.
func (c *Collection) Len() int { return len(c.items) }

// HasKind reports whether at least one error of the given kind exists.
func (c *Collection) HasKind(kind Kind) bool {
	for _, e := range c.items {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// FirstOffset returns the smallest locatable offset for a source, or
// Unknown when every error for that source lacks an offset.
func (c *Collection) FirstOffset(source string) int64 {
	best := Unknown
	for _, e := range c.items {
		if e.Source != source || e.Offset == Unknown {
			continue
		}
		if best == Unknown || e.Offset < best {
			best = e.Offset
		}
	}
	return best
}

// Error renders the collection as one diagnostic string.
func (c *Collection) Error() string {
	if len(c.items) == 0 {
		return "no errors"
	}
	parts := make([]string, len(c.items))
	for i, e := range c.items {
		parts[i] = e.Error()
	}
	return fmt.Sprintf("%d error(s): %s", len(c.items), strings.Join(parts, "; "))
}
