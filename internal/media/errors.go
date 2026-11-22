package media

import "fmt"

// Kind classifies errors so stages can apply policy (skip, degrade, abort).
type Kind uint8

const (
	KindCorrupt   Kind = iota // payload/metadata failed validation
	KindChecksum              // SHA-256 mismatch
	KindTruncated             // byte length shorter than declared
	KindOverflow              // arithmetic overflow in time/offset math
	KindRange                 // index, offset or timestamp out of bounds
	KindState                 // illegal state transition (e.g. rewrite committed)
	KindCanceled              // queue/stage canceled by backpressure or caller
)

func (k Kind) String() string {
	switch k {
	case KindCorrupt:
		return "corrupt"
	case KindChecksum:
		return "checksum"
	case KindTruncated:
		return "truncated"
	case KindOverflow:
		return "overflow"
	case KindRange:
		return "range"
	case KindState:
		return "state"
	case KindCanceled:
		return "canceled"
	}
	return "unknown"
}

// Error carries track, sequence and offset context across all stages.
type Error struct {
	Kind   Kind
	Track  uint32 // stream/track id, 0 when not applicable
	Seq    uint64 // frame or packet sequence, 0 when not applicable
	Offset int64  // byte or sample offset, -1 when not applicable
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("media/%s track=%d seq=%d off=%d: %s", e.Kind, e.Track, e.Seq, e.Offset, e.Msg)
}

// NewError builds an Error with the shared context fields.
func NewError(kind Kind, track uint32, seq uint64, off int64, format string, args ...any) *Error {
	return &Error{Kind: kind, Track: track, Seq: seq, Offset: off, Msg: fmt.Sprintf(format, args...)}
}

// Recovery describes the recovery state of a track or stage.
type Recovery uint8

const (
	RecoveryNone     Recovery = iota // no fault observed
	RecoveryDegraded                 // faults observed, policy: continue
	RecoveryRecovered                // fault observed then healed
	RecoveryFailed                   // unrecoverable, stage aborted
)

func (r Recovery) String() string {
	switch r {
	case RecoveryNone:
		return "none"
	case RecoveryDegraded:
		return "degraded"
	case RecoveryRecovered:
		return "recovered"
	case RecoveryFailed:
		return "failed"
	}
	return "unknown"
}
