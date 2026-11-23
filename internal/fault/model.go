// Package fault defines the shared error model for the media processing
// pipeline. Every damaged packet, corrupt index, or unusable metadata record
// is expressed as a Fault that carries a byte offset into the input stream so
// that operators can jump straight to the first suspicious byte.
package fault

import (
	"errors"
	"fmt"
)

// Kind classifies what went wrong.
type Kind string

const (
	KindLoss        Kind = "loss"         // a sequence-number gap
	KindReordered   Kind = "reordered"    // packet arrived after a higher sequence number
	KindDuplicate   Kind = "duplicate"    // same sequence number seen twice
	KindTruncated   Kind = "truncated"    // packet shorter than its header/payload claims
	KindRTCPInvalid Kind = "rtcp_invalid" // RTCP report missing or non-monotonic
	KindIndexDigest Kind = "index_digest" // index bound to a different input digest
	KindLayout      Kind = "layout"       // unknown or inconsistent audio layout
	KindSampleRate  Kind = "sample_rate"  // non-integral resampling boundary
	KindColor       Kind = "color"        // conflicting color metadata below priority
	KindAlignment   Kind = "alignment"    // no keyframe within the search budget
	KindCheckpoint  Kind = "checkpoint"   // checkpoint unusable after restart
)

// Recovery is the strategy the runner applies to a fault.
type Recovery string

const (
	RecoverContinue Recovery = "continue" // skip the affected packet and keep going
	RecoverResync   Recovery = "resync"   // jump forward to the next usable keyframe
	RecoverRestart  Recovery = "restart"  // roll back to a previous checkpoint
	RecoverAbort    Recovery = "abort"    // stop: unrecoverable
)

// Severity expresses whether processing can continue past a fault.
type Severity int

const (
	SeverityWarning  Severity = iota // output remains usable
	SeverityDamaged                  // output is usable only after recovery
	SeverityFatal                    // no usable output
)

func (s Severity) String() string {
	switch s {
	case SeverityWarning:
		return "warning"
	case SeverityDamaged:
		return "damaged"
	case SeverityFatal:
		return "fatal"
	default:
		return "unknown"
	}
}

// Fault is a located problem in the input or in derived metadata.
type Fault struct {
	Kind      Kind     `json:"kind"`
	TrackID   uint32   `json:"track_id,omitempty"`
	// Offset is the first byte of the offending packet or record in the input.
	Offset    int64    `json:"offset"`
	// Length is the byte span the offset refers to, when known.
	Length    int    `json:"length,omitempty"`
	Detail    string   `json:"detail,omitempty"`
	Recovery  Recovery `json:"recovery"`
	Severity  Severity `json:"-"`
}

// Error implements the error interface so a Fault can be returned directly.
func (f *Fault) Error() string {
	loc := fmt.Sprintf("offset=%d", f.Offset)
	if f.Length > 0 {
		loc += fmt.Sprintf(" len=%d", f.Length)
	}
	if f.TrackID != 0 {
		return fmt.Sprintf("%s fault (track=%d %s): %s [%s]", f.Kind, f.TrackID, loc, f.Detail, f.Recovery)
	}
	return fmt.Sprintf("%s fault (%s): %s [%s]", f.Kind, loc, f.Detail, f.Recovery)
}

// WithDetail returns a copy of the fault with an explanatory detail message.
func (f *Fault) WithDetail(format string, args ...any) *Fault {
	g := *f
	g.Detail = fmt.Sprintf(format, args...)
	return &g
}

// IsKind reports whether err is a *Fault of the given kind.
func IsKind(err error, kind Kind) bool {
	var f *Fault
	return errors.As(err, &f) && f.Kind == kind
}

// As returns the first *Fault in err's chain, if any.
func As(err error) (*Fault, bool) {
	var f *Fault
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}

// ExitCode maps a fault to the CLI exit code contract:
// 2 = input damaged but precisely located, 3 = unrecoverable metadata or
// alignment error, 4 = checkpoint/digest mismatch.
func ExitCode(err error) int {
	f, ok := As(err)
	if !ok {
		if err == nil {
			return 0
		}
		return 3
	}
	switch f.Kind {
	case KindLoss, KindReordered, KindDuplicate, KindTruncated, KindRTCPInvalid:
		return 2
	case KindCheckpoint:
		return 4
	default:
		return 3
	}
}
