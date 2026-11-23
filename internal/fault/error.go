// Package fault defines the typed error model shared by every stage. Every
// error carries the byte offset of the offending structure inside its source
// so wrong results can be traced back to the original bytes.
package fault

import (
	"fmt"
	"strings"
)

// Kind classifies an error.
type Kind string

const (
	KindTruncated     Kind = "truncated_unit"
	KindUnknown       Kind = "unknown_structure"
	KindLimit         Kind = "size_limit_exceeded"
	KindTimestampWrap Kind = "timestamp_wrap"
	KindDrift         Kind = "clock_drift"
	KindLatePacket    Kind = "late_packet"
	KindVFRDupPTS     Kind = "vfr_duplicate_pts"
	KindMissingFrame  Kind = "missing_frame"
	KindColorConflict Kind = "color_conflict"
	KindInvariant     Kind = "invariant_violation"
	KindInvalidInput  Kind = "invalid_input"
)

// Error is the single error type used across the pipeline.
type Error struct {
	Kind       Kind   `json:"kind"`
	Message    string `json:"message"`
	SourceFile string `json:"source_file,omitempty"`
	// Offset is the byte offset of the offending unit; -1 when unavailable.
	Offset int64 `json:"source_error_offset"`
	// Recoverable means the pipeline can skip the unit and continue.
	Recoverable bool `json:"recoverable"`
	// Recovered is set when recovery mode successfully resynchronized past it.
	Recovered bool `json:"recovered,omitempty"`
}

// Error implements the error interface.
func (e *Error) Error() string {
	rec := ""
	if e.Recoverable {
		rec = " [recoverable]"
	}
	return fmt.Sprintf("%s: %s at %s:%d%s", e.Kind, e.Message, e.SourceFile, e.Offset, rec)
}

// New constructs an error with an explicit source offset.
func New(kind Kind, msg, file string, offset int64, recoverable bool) *Error {
	return &Error{Kind: kind, Message: msg, SourceFile: file, Offset: offset, Recoverable: recoverable}
}

// Batch is an ordered collection of typed errors.
type Batch struct {
	errs []*Error
}

// Add appends an error and returns it.
func (b *Batch) Add(e *Error) *Error {
	b.errs = append(b.errs, e)
	return e
}

// All returns the collected errors in encounter order.
func (b *Batch) All() []*Error { return b.errs }

// Len returns the number of collected errors.
func (b *Batch) Len() int { return len(b.errs) }

// Recoverable returns the subset flagged recoverable.
func (b *Batch) Recoverable() []*Error {
	var out []*Error
	for _, e := range b.errs {
		if e.Recoverable {
			out = append(out, e)
		}
	}
	return out
}

// Fatal returns the subset flagged non-recoverable.
func (b *Batch) Fatal() []*Error {
	var out []*Error
	for _, e := range b.errs {
		if !e.Recoverable {
			out = append(out, e)
		}
	}
	return out
}

// RecoveredCount counts errors that recovery mode resynchronized past.
func (b *Batch) RecoveredCount() int {
	n := 0
	for _, e := range b.errs {
		if e.Recovered {
			n++
		}
	}
	return n
}

// LastOffset returns the offset of the last error, or -1 when empty.
func (b *Batch) LastOffset() int64 {
	if len(b.errs) == 0 {
		return -1
	}
	return b.errs[len(b.errs)-1].Offset
}

// HasKind reports whether any error of the given kind was collected.
func (b *Batch) HasKind(k Kind) bool {
	for _, e := range b.errs {
		if e.Kind == k {
			return true
		}
	}
	return false
}

// containerDamageKinds are corruption classes intrinsic to the input
// containers. They drive the exit-code-2 policy; network and clock anomalies
// (late packets, wraps, drift) are expected session conditions, not damage.
var containerDamageKinds = map[Kind]bool{
	KindTruncated:     true,
	KindUnknown:       true,
	KindColorConflict: true,
	KindVFRDupPTS:     true,
}

// HasContainerDamage reports whether a container-level corruption fault was
// collected and recovered around.
func (b *Batch) HasContainerDamage() bool {
	for _, e := range b.errs {
		if containerDamageKinds[e.Kind] {
			return true
		}
	}
	return false
}

func (b *Batch) Error() string {
	parts := make([]string, 0, len(b.errs))
	for _, e := range b.errs {
		parts = append(parts, e.Error())
	}
	return strings.Join(parts, "; ")
}
