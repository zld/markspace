// Package fault defines the unified error model shared by all subsystems.
// Every anomaly (negative start, wraparound, non-monotonic timestamps,
// truncation, loss, desync, ...) is recorded as a Fault carrying a stable
// error code and, where applicable, the byte offset in the source file.
package fault

import (
	"fmt"
	"strings"
	"sync"
)

// Code is a stable machine-readable error identifier.
type Code string

const (
	ErrTruncated        Code = "TRUNCATED_CONTAINER"
	ErrNonMonotonic     Code = "NON_MONOTONIC_TS"
	ErrNegativeStart    Code = "NEGATIVE_START"
	ErrWraparound       Code = "TS_WRAPAROUND"
	ErrUnknownMajor     Code = "UNKNOWN_PARSER_MAJOR"
	ErrUnsupportedTrack Code = "UNSUPPORTED_TRACK"
	ErrPacketLoss       Code = "PACKET_LOSS"
	ErrUnrecoverable    Code = "UNRECOVERABLE_RANGE"
	ErrDesync           Code = "AV_DESYNC"
	ErrBitrateDelta     Code = "BITRATE_DELTA_EXCEEDED"
	ErrBackpressure     Code = "BACKPRESSURE"
	ErrBufferOverflow   Code = "BUFFER_OVERFLOW"
	ErrManifestDrift    Code = "MANIFEST_DRIFT"
	ErrCanceled         Code = "CANCELED"
)

// Fault is a single recorded anomaly. Offset is the byte offset in the
// input file the fault refers to, or -1 when not applicable.
type Fault struct {
	Code   Code
	Offset int64
	Detail string
}

func (f Fault) Error() string {
	if f.Offset >= 0 {
		return fmt.Sprintf("%s @offset=%d: %s", f.Code, f.Offset, f.Detail)
	}
	return fmt.Sprintf("%s: %s", f.Code, f.Detail)
}

// List is a goroutine-safe collector of faults.
type List struct {
	mu     sync.Mutex
	faults []Fault
}

func NewList() *List { return &List{} }

// Add records a fault. offset may be -1 when not tied to a file position.
func (l *List) Add(code Code, offset int64, format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.faults = append(l.faults, Fault{Code: code, Offset: offset, Detail: fmt.Sprintf(format, args...)})
}

// All returns a copy of the recorded faults in insertion order.
func (l *List) All() []Fault {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]Fault, len(l.faults))
	copy(out, l.faults)
	return out
}

// Has reports whether at least one fault with the given code was recorded.
func (l *List) Has(code Code) bool { return l.Count(code) > 0 }

// Count returns how many faults carry the given code.
func (l *List) Count(code Code) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, f := range l.faults {
		if f.Code == code {
			n++
		}
	}
	return n
}

// Len returns the total number of recorded faults.
func (l *List) Len() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.faults)
}

func (l *List) String() string {
	parts := make([]string, 0, l.Len())
	for _, f := range l.All() {
		parts = append(parts, f.Error())
	}
	return strings.Join(parts, "; ")
}
