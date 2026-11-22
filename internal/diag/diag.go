// Package diag defines the structured fault carried through every stage of
// the archive pipeline. A fault always names a stable machine code and a
// concrete offset so an operator can locate the damaged bytes or element.
package diag

import (
	"errors"
	"fmt"
)

// Stable fault codes. They are part of the CLI contract and are covered by
// tests, so do not reword them.
const (
	CodeInvalidTime       = "invalid_time"
	CodeTruncated         = "truncated_records"
	CodeAmbiguousDF       = "ambiguous_drop_frame"
	CodeTimestampRollback = "timestamp_rollback"
	CodeMissingBase       = "missing_base"
	CodeRTPSegmentGap     = "rtp_sequence_gap"
	CodeRTPDuplicate      = "rtp_duplicate"
	CodeRTPBurst          = "rtp_timestamp_jump"
	CodeMP4Truncated      = "mp4_truncated"
	CodeMP4Structure      = "mp4_structure"
	CodeVFRGap            = "vfr_interval_anomaly"
	CodeQueueFull         = "queue_full"
	CodeCanceled          = "canceled"
)

// Fault is an auditable, offset-bearing error. Offset is a byte offset for
// text and MP4 sources, and an element/sequence index for packet streams.
type Fault struct {
	Code     string
	Offset   int64
	MediaMs  float64
	HasMedia bool
	Detail   string
	Err      error
}

// New builds a fault with a formatted detail.
func New(code string, offset int64, format string, args ...any) *Fault {
	return &Fault{Code: code, Offset: offset, Detail: fmt.Sprintf(format, args...)}
}

// WithMedia attaches a media timestamp in milliseconds treated as time on the
// unified time base.
func (f *Fault) WithMedia(ms float64) *Fault {
	f.MediaMs, f.HasMedia = ms, true
	return f
}

// AtOffset returns a copy of f moved to a different source offset.
func (f *Fault) AtOffset(off int64) *Fault {
	cp := *f
	cp.Offset = off
	return &cp
}

func (f *Fault) Error() string {
	if f.HasMedia {
		return fmt.Sprintf("%s at offset %d (media %.3f ms): %s", f.Code, f.Offset, f.MediaMs, f.Detail)
	}
	return fmt.Sprintf("%s at offset %d: %s", f.Code, f.Offset, f.Detail)
}

// Unwrap exposes an optional wrapped cause.
func (f *Fault) Unwrap() error { return f.Err }

// As extracts the first *Fault in err's chain.
func As(err error) (*Fault, bool) {
	var f *Fault
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}

// IsCode reports whether err is or wraps a fault with the given code.
func IsCode(err error, code string) bool {
	f, ok := As(err)
	return ok && f.Code == code
}
