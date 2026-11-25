// Package model defines shared errors, time bases and domain types used by
// every subsystem of the media processing kernel.
package model

import "fmt"

// Sentinel errors. Callers use errors.Is to classify failures.
var (
	// ErrSyncByte means an MPEG-TS packet did not start with 0x47.
	ErrSyncByte = fmt.Errorf("ts: missing sync byte 0x47")
	// ErrTruncated means the input ended with fewer than 188 bytes remaining.
	ErrTruncated = fmt.Errorf("ts: truncated trailing packet")
	// ErrNoMapping means no PMT currently maps a referenced PID.
	ErrNoMapping = fmt.Errorf("psi: no program mapping for pid")
	// ErrBlocked means an output cannot proceed because a dependency is missing.
	ErrBlocked = fmt.Errorf("dag: blocked on missing dependency")
	// ErrNotFound is returned for unknown checkpoints or manifests.
	ErrNotFound = fmt.Errorf("state: not found")
	// ErrChecksum means a verification digest did not match the manifest.
	ErrChecksum = fmt.Errorf("verify: checksum mismatch")
)

// Exit codes used by the command line entry point.
const (
	ExitOK        = 0
	ExitMediaFail = 1
	ExitUsage     = 2
)

// CorruptError describes a transport-stream defect located at a byte offset.
// It always carries the absolute input offset and, when known, the affected
// PID so failures can be propagated to the timeline and to segments.
type CorruptError struct {
	Kind     string // "sync" | "truncated" | "continuity" | "pcr" | "psi"
	Offset   int64  // absolute byte offset in the input
	PID      uint16 // affected PID, or 0 when unknown
	Expected uint16 // expected value (e.g. continuity counter), 0 when unused
	Actual   uint16 // observed value, 0 when unused
	Detail   string
}

func (e *CorruptError) Error() string {
	if e.PID != 0 {
		return fmt.Sprintf("%s corruption at byte offset %d (pid %#x): %s",
			e.Kind, e.Offset, e.PID, e.Detail)
	}
	return fmt.Sprintf("%s corruption at byte offset %d: %s",
		e.Kind, e.Offset, e.Detail)
}

// Is lets errors.Is(err, ErrSyncByte) match a typed sync-byte failure.
func (e *CorruptError) Is(target error) bool {
	switch target {
	case ErrSyncByte:
		return e.Kind == "sync"
	case ErrTruncated:
		return e.Kind == "truncated"
	case ErrNoMapping:
		return e.Kind == "psi"
	default:
		return false
	}
}

// Gap marks a continuity-counter gap observed on one PID. Repeated packets
// (same counter, payload start flag unchanged) are reported as Repeated and
// may be ignored downstream.
type Gap struct {
	Offset   int64
	PID      uint16
	Expected uint8
	Actual   uint8
	Repeated bool
}
