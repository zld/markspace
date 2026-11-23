// Package clock defines the shared media clock model: RTP sequence distance,
// timestamp unwrap across 32-bit wrap, bounded resource limits and a single
// error classification used by the ingest, recovery and index subsystems.
package clock

import (
	"errors"
	"math"
)

// DefaultClockRate is the RTP clock rate (Hz) used for the generated video
// sample streams, matching the conventional H.264/H.265 video rate.
const DefaultClockRate uint32 = 90000

// Bounded limits shared across subsystems. Every parse length, queue capacity
// and index offset must be checked against one of these limits.
const (
	MaxPacketBytes     = 1 << 20 // 1 MiB cap on any single parsed packet
	MaxQueueDepth      = 1 << 16 // reorder / retransmit window cap
	MaxIndexEntries    = 1 << 24 // per-track sample table cap
	MaxManifestSegs    = 1 << 16 // manifest segment cap
	MaxTracks          = 32
	MaxSRPoints        = 1 << 14 // sender-report samples retained per source
	MaxDurationMicros  = int64(24 * 3600 * 1_000_000)
	MaxMP4FileBytes    = int64(1) << 40 // 1 TiB sanity bound
	Uint32Mod   int64  = 1 << 32
	Uint16Mod   uint32 = 1 << 16
)

// ErrorKind enumerates the classified failure modes understood by more than
// one subsystem.
type ErrorKind int

const (
	ErrNone ErrorKind = iota
	ErrInvalidLength
	ErrOverflow
	ErrWrap
	ErrBackwards
	ErrDuplicate
	ErrOutOfWindow
	ErrTruncated
	ErrMissingKeyframe
	ErrIndexOutOfBounds
	ErrUnrecoverable
)

// String renders an ErrorKind for reports and terminal output.
func (k ErrorKind) String() string {
	switch k {
	case ErrNone:
		return "none"
	case ErrInvalidLength:
		return "invalid_length"
	case ErrOverflow:
		return "integer_overflow"
	case ErrWrap:
		return "timestamp_wrap"
	case ErrBackwards:
		return "backwards_timestamp"
	case ErrDuplicate:
		return "duplicate_packet"
	case ErrOutOfWindow:
		return "outside_window"
	case ErrTruncated:
		return "truncated_sample"
	case ErrMissingKeyframe:
		return "missing_keyframe"
	case ErrIndexOutOfBounds:
		return "index_out_of_bounds"
	case ErrUnrecoverable:
		return "unrecoverable"
	default:
		return "unknown"
	}
}

// Error is the canonical classified error carried between subsystems.
type Error struct {
	Kind     ErrorKind
	Message  string
	Sequence uint16 // offending RTP sequence number when applicable
	SSRC     uint32
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	s := e.Kind.String()
	if e.Message != "" {
		s += ": " + e.Message
	}
	return s
}

// NewError builds a classified error.
func NewError(kind ErrorKind, msg string) *Error {
	return &Error{Kind: kind, Message: msg}
}

// AsError extracts a *clock.Error from any error path.
func AsError(err error) (*Error, bool) {
	var ce *Error
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}

// SeqDistance returns the signed forward distance from a to b in 16-bit RTP
// sequence space. Positive values mean b is ahead of a; the result lies in
// [-32768, 32767].
func SeqDistance(a, b uint16) int32 {
	d := int32(uint32(b) - uint32(a))
	half := int32(Uint16Mod) / 2
	if d > half {
		d -= int32(Uint16Mod)
	} else if d < -half {
		d += int32(Uint16Mod)
	}
	return d
}

// UnwrapTS extends a 32-bit RTP timestamp against the previous extended
// value. It returns the new extended timestamp and the number of forward
// wraps detected (0 or 1 for well-formed streams). Backwards jumps beyond one
// full period are classified rather than silently accepted.
func UnwrapTS(prevExtended int64, cur uint32) (int64, int, *Error) {
	base := uint32(prevExtended)
	d := int64(cur) - int64(base)
	if d > math.MaxInt32 {
		d -= Uint32Mod
	}
	if d < math.MinInt32 {
		d += Uint32Mod
	}
	next := prevExtended + d
	wraps := 0
	if cur < base && d > 0 {
		wraps = 1
	}
	if d < 0 && (base-cur) > math.MaxUint32/2 {
		return 0, 0, NewError(ErrBackwards, "rtp timestamp jumped backwards beyond one period")
	}
	return next, wraps, nil
}

// Add64 adds two int64 values with overflow detection.
func Add64(a, b int64) (int64, *Error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, NewError(ErrOverflow, "int64 addition overflow")
	}
	return a + b, nil
}

// Mul64 multiplies two int64 values with overflow detection.
func Mul64(a, b int64) (int64, *Error) {
	if a == 0 || b == 0 {
		return 0, nil
	}
	r := a * b
	if r/a != b {
		return 0, NewError(ErrOverflow, "int64 multiplication overflow")
	}
	return r, nil
}

// CheckOffset verifies that offset+length stays within bound without overflow.
func CheckOffset(offset, length, bound int64) *Error {
	if offset < 0 || length < 0 || bound < 0 {
		return NewError(ErrIndexOutOfBounds, "negative offset or length")
	}
	end, err := Add64(offset, length)
	if err != nil {
		return err
	}
	if end > bound {
		return NewError(ErrIndexOutOfBounds, "index offset exceeds container bound")
	}
	return nil
}
