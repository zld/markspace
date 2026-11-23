// Package errs defines the unified error classification shared by every
// subsystem (clock, rtp, timeline, subtitle, segment, bitrate, pipeline).
package errs

import "fmt"

// Kind is the stable, machine-readable error category. Exit-code mapping in
// the command adapter: input categories -> 2, internal categories -> 1.
type Kind int

const (
	KindUnknown Kind = iota
	// KindInvalidInput covers malformed parameters and corrupt user data.
	KindInvalidInput
	// KindLimitExceeded covers the hard limits (duration, text size, cache).
	KindLimitExceeded
	// KindChecksum covers failed integrity checks (e.g. RTP payload CRC).
	KindChecksum
	// KindTimestamp covers wrap/rollover inconsistencies and illegal codes.
	KindTimestamp
	// KindSealed covers data rejected because its interval was archived.
	KindSealed
	// KindInternal covers invariant violations that must never happen.
	KindInternal
)

// Error is the single error type used across the pipeline. It carries the
// classification kind, the subsystem that raised it and a stable detail.
type Error struct {
	Kind Kind
	Area string
	Msg  string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s: %s", e.Kind, e.Area, e.Msg)
}

// New builds a classified error.
func New(kind Kind, area, format string, args ...any) error {
	return &Error{Kind: kind, Area: area, Msg: fmt.Sprintf(format, args...)}
}

// Class returns the kind of any error, defaulting to KindInternal.
func Class(err error) Kind {
	if err == nil {
		return KindUnknown
	}
	if e, ok := err.(*Error); ok {
		return e.Kind
	}
	return KindInternal
}

// String renders the kind for reports and manifests.
func (k Kind) String() string {
	switch k {
	case KindInvalidInput:
		return "invalid_input"
	case KindLimitExceeded:
		return "limit_exceeded"
	case KindChecksum:
		return "checksum"
	case KindTimestamp:
		return "timestamp"
	case KindSealed:
		return "sealed"
	case KindInternal:
		return "internal"
	default:
		return "unknown"
	}
}
