// Package clock provides the shared integer-nanosecond media time basis,
// RTP timestamp unwrapping, drift checks, and the uniform error type used by
// the container, event, chunk, index, and batch modules.
package clock

import (
	"fmt"
	"math"
)

// BasisVersion is persisted in every index. Bump it if the conversion rules
// change in a way that can alter a computed boundary.
const BasisVersion = "integer-ns/v1"

// NanosecondsPerMillisecond is the shared reporting scale.
const (
	NanosecondsPerMillisecond = int64(1_000_000)
	NanosecondsPerSecond      = int64(1_000_000_000)
	DriftThresholdNS          = 2 * NanosecondsPerMillisecond
)

// Basis describes the common timeline used by every track and event.
type Basis struct {
	Version string `json:"version"`
	Unit    string `json:"unit"`
}

// SharedBasis returns the project-wide integer nanosecond basis.
func SharedBasis() Basis {
	return Basis{Version: BasisVersion, Unit: "nanosecond"}
}

// Timebase preserves an original media time base and its conversion to the
// shared basis.
type Timebase struct {
	Timescale uint32 `json:"timescale"`
	RawStart  int64  `json:"raw_start,omitempty"`
	RawEnd    int64  `json:"raw_end,omitempty"`
	StartNS   int64  `json:"start_ns"`
	EndNS     int64  `json:"end_ns"`
}

// Convert converts a raw timestamp in timescale ticks to integer
// nanoseconds. It uses quotient/remainder arithmetic so valid media-sized
// values cannot overflow before the overflow check is reached.
func Convert(raw int64, timescale uint32) (int64, error) {
	if timescale == 0 {
		return 0, NewError(CodeInvalidTimebase, "timescale must not be zero", -1)
	}
	if raw < 0 {
		return 0, NewError(CodeInvalidTimestamp, fmt.Sprintf("raw timestamp %d is negative", raw), -1)
	}

	ts := int64(timescale)
	seconds := raw / ts
	remainder := raw % ts
	if seconds > math.MaxInt64/NanosecondsPerSecond {
		return 0, NewError(CodeOverflow, "nanosecond conversion overflows", -1)
	}
	ns := seconds * NanosecondsPerSecond
	extra := remainder * NanosecondsPerSecond / ts
	if extra > math.MaxInt64-ns {
		return 0, NewError(CodeOverflow, "nanosecond conversion overflows", -1)
	}
	return ns + extra, nil
}

// NewTimebase builds a Timebase while retaining both raw and converted ends.
func NewTimebase(timescale uint32, rawStart, rawEnd int64) (Timebase, error) {
	startNS, err := Convert(rawStart, timescale)
	if err != nil {
		return Timebase{}, err
	}
	endNS, err := Convert(rawEnd, timescale)
	if err != nil {
		return Timebase{}, err
	}
	return Timebase{
		Timescale: timescale,
		RawStart:  rawStart,
		RawEnd:    rawEnd,
		StartNS:   startNS,
		EndNS:     endNS,
	}, nil
}

// DurationNS returns the converted span.
func (t Timebase) DurationNS() int64 { return t.EndNS - t.StartNS }

// AbsNS returns the absolute value of a nanosecond difference.
func AbsNS(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// DriftNS returns the absolute drift between two converted endpoints.
func DriftNS(expectedNS, actualNS int64) int64 {
	return AbsNS(expectedNS - actualNS)
}

// DriftExceeded reports whether drift is strictly greater than 2 ms.
func DriftExceeded(driftNS int64) bool { return driftNS > DriftThresholdNS }

// Frame is one decoded sample on a timeline. DTSNS is its decode start time.
type Frame struct {
	Index       int    `json:"index"`
	DTSRaw      int64  `json:"dts_raw"`
	DTSNS       int64  `json:"dts_ns"`
	DurationRaw uint32 `json:"duration_raw,omitempty"`
	DurationNS  int64  `json:"duration_ns"`
	ByteOffset  int64  `json:"byte_offset,omitempty"`
}

// Timeline is a track converted to the shared basis.
type Timeline struct {
	TrackID  uint32   `json:"track_id"`
	Timebase Timebase `json:"timebase"`
	Frames   []Frame  `json:"frames"`
}

// Boundary returns the first decodable frame at or after atNS and its
// nonnegative quantization error. If atNS is after the last frame start,
// ok is false.
func (t Timeline) Boundary(atNS int64) (frame Frame, index int, quantizeNS int64, ok bool) {
	for i, f := range t.Frames {
		if f.DTSNS >= atNS {
			return f, i, f.DTSNS - atNS, true
		}
	}
	return Frame{}, -1, 0, false
}
