// Package clock defines the shared time base for MPEG-TS PCR/PTS/DTS repair.
//
// Every timestamp handled by tsforge is expressed in 90 kHz ticks. PCR values
// are carried at 27 MHz and wrap on the 33-bit PCR base; PTS/DTS wrap on their
// own 33-bit field. The unwrap stage converts both into a single monotonic
// int64 timeline so downstream indexing and muxing never see a rollover.
package clock

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

const (
	// TimeScale is the common MPEG-2 system clock rate in ticks per second.
	TimeScale int64 = 90000
	// WrapPeriod is the size of the 33-bit PCR/PTS/DTS field.
	WrapPeriod int64 = 1 << 33
	// PCRToTicks converts a 27 MHz PCR base value to 90 kHz ticks.
	PCRToTicks int64 = 300
)

// ErrorKind classifies unrecoverable or suspicious timestamp situations.
type ErrorKind string

const (
	KindAmbiguous      ErrorKind = "ambiguous_timestamp"
	KindNonMonotonic   ErrorKind = "non_monotonic_timestamp"
	KindReorder        ErrorKind = "reorder_window_exceeded"
	KindAVOrder        ErrorKind = "av_relative_order"
	KindMissing        ErrorKind = "missing_timestamp"
	KindUnknownProgram ErrorKind = "unknown_program"
	KindSampleLimit    ErrorKind = "sample_limit"
	KindDiscontinuity  ErrorKind = "timestamp_discontinuity"
)

// Error is a timestamp repair diagnostic. Offsets are byte offsets in the
// source TS file so an operator can jump straight to the offending packet.
type Error struct {
	Kind        ErrorKind
	PID         uint16
	Offset      int64
	PacketIndex int64
	Raw         uint64
	Detail      string
	Blocking    bool
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("%s pid=0x%04x offset=%d packet=%d raw=%d: %s",
		e.Kind, e.PID, e.Offset, e.PacketIndex, e.Raw, e.Detail)
}

// ErrorList is an ordered collection of timestamp errors.
type ErrorList []*Error

// Sort orders diagnostics by source offset, then kind.
func (l ErrorList) Sort() {
	sort.SliceStable(l, func(i, j int) bool {
		if l[i].Offset != l[j].Offset {
			return l[i].Offset < l[j].Offset
		}
		return l[i].Kind < l[j].Kind
	})
}

// Blocking returns only diagnostics that must stop downstream work.
func (l ErrorList) Blocking() ErrorList {
	var out ErrorList
	for _, e := range l {
		if e.Blocking {
			out = append(out, e)
		}
	}
	return out
}

// Err returns a *BlockedError when at least one blocking diagnostic exists.
func (l ErrorList) Err() error {
	b := l.Blocking()
	if len(b) == 0 {
		return nil
	}
	return &BlockedError{Errors: b}
}

// BlockedError reports that timestamps cannot be inferred reliably. The
// pipeline must not fabricate values for these samples.
type BlockedError struct {
	Errors ErrorList
}

// Error implements error.
func (b *BlockedError) Error() string {
	if len(b.Errors) == 0 {
		return "timestamp repair blocked"
	}
	parts := make([]string, 0, len(b.Errors))
	for _, e := range b.Errors {
		parts = append(parts, e.Error())
	}
	return "timestamp repair blocked: " + strings.Join(parts, "; ")
}

// IsBlocked reports whether err (possibly wrapped) describes a blocking
// timestamp condition.
func IsBlocked(err error) (ErrorList, bool) {
	var be *BlockedError
	if errors.As(err, &be) {
		return be.Errors, len(be.Errors) > 0
	}
	return nil, false
}

// Config holds plausibility limits used while unwrapping. Limits are about
// detection, not about guessing: values outside them become blocking errors.
type Config struct {
	// MaxJump is the largest accepted forward step between consecutive
	// timestamps of one stream, in 90 kHz ticks. The default is 5 seconds.
	MaxJump int64
	// MaxReorder is the largest accepted backward step for PTS, in ticks.
	// It absorbs B-frame reordering. The default is 1 second.
	MaxReorder int64
	// MaxAVDelta is the largest accepted offset between the first video and
	// first audio timestamp on the shared timeline, in ticks.
	MaxAVDelta int64
}

// DefaultConfig returns production-sane plausibility limits.
func DefaultConfig() Config {
	return Config{
		MaxJump:    5 * TimeScale,
		MaxReorder: TimeScale,
		MaxAVDelta: 5 * TimeScale,
	}
}

// Normalize fills unset limits with defaults.
func (c Config) Normalize() Config {
	d := DefaultConfig()
	if c.MaxJump <= 0 {
		c.MaxJump = d.MaxJump
	}
	if c.MaxReorder <= 0 {
		c.MaxReorder = d.MaxReorder
	}
	if c.MaxAVDelta <= 0 {
		c.MaxAVDelta = d.MaxAVDelta
	}
	return c
}

// Ticks converts a 27 MHz PCR base into 90 kHz ticks (truncating sub-tick
// remainder; PCR extension is not used for the shared timeline).
func Ticks(pcrBase uint64) int64 {
	return int64(pcrBase / uint64(PCRToTicks))
}

// ForwardVersion reports whether newVersion is a plausible successor of
// currentVersion modulo 32. Distance 1..16 is forward; 0 is a duplicate and
// 17..31 is stale. This makes 31 -> 0 a forward transition, not a rollback.
func ForwardVersion(current, next uint8) bool {
	diff := int(next) - int(current)
	if diff < 0 {
		diff += 32
	}
	return diff >= 1 && diff <= 16
}
