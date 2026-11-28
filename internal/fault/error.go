// Package fault defines the unified error model used by every mediasync
// subsystem. Each fault carries a stable numeric code, the name of the track
// or stream it belongs to and the original byte offset inside the input file
// or packet stream where the problem was observed.
package fault

import (
	"errors"
	"fmt"
)

// Code is a stable numeric error code. Numeric values are part of the output
// contract and must never be reused for a different meaning.
type Code int

const (
	// OK means no fault.
	OK Code = 0
	// ErrTruncated means the input ends in the middle of a declared structure.
	ErrTruncated Code = 1001
	// ErrOutOfBounds means an offset points outside the file/stream.
	ErrOutOfBounds Code = 1002
	// ErrIllegalTimebase means a timescale/clock rate is zero or unsupported.
	ErrIllegalTimebase Code = 1003
	// ErrSampleRange means sample table counts or ranges are inconsistent.
	ErrSampleRange Code = 1004
	// ErrChunkBoundary means a chunk/sample does not end on a declared boundary.
	ErrChunkBoundary Code = 1005
	// ErrContainerDecl means a mandatory container declaration is missing/bad.
	ErrContainerDecl Code = 1006
	// ErrWrapAmbiguous means a timestamp wrap cannot be decided uniquely.
	ErrWrapAmbiguous Code = 2001
	// ErrDriftTooLarge means drift exceeds the configured calibration range.
	ErrDriftTooLarge Code = 2002
	// ErrPacketMalformed means an RTP packet cannot be parsed.
	ErrPacketMalformed Code = 3001
	// ErrPacketUnrecovered means a missing packet could not be reconstructed.
	ErrPacketUnrecovered Code = 3002
	// ErrLimitExceeded means a configured safety limit was reached.
	ErrLimitExceeded Code = 4001
	// ErrOverflow means an offset or length arithmetic overflowed.
	ErrOverflow Code = 4002
	// ErrCanceled means processing was canceled via context.
	ErrCanceled Code = 4003
)

// String returns the stable machine-readable name of a code.
func (c Code) String() string {
	switch c {
	case OK:
		return "ok"
	case ErrTruncated:
		return "truncated"
	case ErrOutOfBounds:
		return "out_of_bounds"
	case ErrIllegalTimebase:
		return "illegal_timebase"
	case ErrSampleRange:
		return "sample_range"
	case ErrChunkBoundary:
		return "chunk_boundary"
	case ErrContainerDecl:
		return "container_declaration"
	case ErrWrapAmbiguous:
		return "wrap_ambiguous"
	case ErrDriftTooLarge:
		return "drift_too_large"
	case ErrPacketMalformed:
		return "packet_malformed"
	case ErrPacketUnrecovered:
		return "packet_unrecovered"
	case ErrLimitExceeded:
		return "limit_exceeded"
	case ErrOverflow:
		return "arithmetic_overflow"
	case ErrCanceled:
		return "canceled"
	default:
		return fmt.Sprintf("unknown_%d", int(c))
	}
}

// Fault is a single, attributable problem. It implements the error interface.
type Fault struct {
	Code   Code   `json:"code"`
	Track  string `json:"track"`
	Offset int64  `json:"offset"`
	Detail string `json:"detail"`
}

// New builds a Fault. Offset is the original byte offset in the input; use -1
// when the fault is not attributable to a specific byte.
func New(code Code, track string, offset int64, format string, args ...any) *Fault {
	return &Fault{
		Code:   code,
		Track:  track,
		Offset: offset,
		Detail: fmt.Sprintf(format, args...),
	}
}

// Error implements error.
func (f *Fault) Error() string {
	if f.Offset >= 0 {
		return fmt.Sprintf("%s(%d) track=%q offset=%d: %s",
			f.Code.String(), int(f.Code), f.Track, f.Offset, f.Detail)
	}
	return fmt.Sprintf("%s(%d) track=%q: %s",
		f.Code.String(), int(f.Code), f.Track, f.Detail)
}

// AsFault extracts the first *Fault from err, if any.
func AsFault(err error) (*Fault, bool) {
	var f *Fault
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}
