package media

import (
	"errors"
	"fmt"
)

// ErrorClass categorizes frame-level errors across modules so that damage
// localization and metrics can aggregate them uniformly.
type ErrorClass string

const (
	ClassChecksum    ErrorClass = "checksum"
	ClassTruncation  ErrorClass = "truncation"
	ClassTimestamp   ErrorClass = "timestamp"
	ClassSequence    ErrorClass = "sequence"
	ClassAudioLayout ErrorClass = "audio_layout"
	ClassSampleRate  ErrorClass = "sample_rate"
	ClassColor       ErrorClass = "color"
	ClassIndex       ErrorClass = "index"
)

// FrameError is a per-frame error that preserves the original stream and
// sequence coordinates of the affected segment.
type FrameError struct {
	Class    ErrorClass
	StreamID uint32
	Seq      uint16
	Detail   string
}

func (e *FrameError) Error() string {
	return fmt.Sprintf("%s: stream=%d seq=%d: %s", e.Class, e.StreamID, e.Seq, e.Detail)
}

// Sentinel errors shared across modules.
var (
	// ErrStaleIndex is returned when an index digest does not match the
	// digest of the current input; the caller must rescan.
	ErrStaleIndex = errors.New("index digest does not match input")
	// ErrIndexTruncated is returned when a persisted index is incomplete or
	// fails its integrity check; the caller must safely rebuild it.
	ErrIndexTruncated = errors.New("index truncated or corrupt")
	// ErrAmbiguousTimestamp is returned when a timestamp cannot be uniquely
	// unwrapped from its sequence number and modulus.
	ErrAmbiguousTimestamp = errors.New("timestamp cannot be uniquely unwrapped")
)
