// Package fault defines the cross-module error model: stable error codes,
// source offsets and recovery hints shared by every subsystem.
package fault

import "fmt"

// Code is a stable, machine-readable error identifier.
type Code string

const (
	// Subtitle parsing.
	CodeNegativeDuration Code = "E_NEGATIVE_DURATION" // cue end <= start
	CodeBadTimecode      Code = "E_BAD_TIMECODE"      // malformed or out-of-range timestamp
	CodeDayOverflow      Code = "E_DAY_OVERFLOW"      // timestamp crosses the 24h day boundary
	CodeTruncatedInput   Code = "E_TRUNCATED_INPUT"   // input ends mid-record
	CodeBadDropFrame     Code = "E_BAD_DF_MARKER"     // dropped-frame number used in DF timecode

	// Time base conversion.
	CodeUnsupportedRate Code = "E_UNSUPPORTED_RATE" // frame rate outside the supported set
	CodeFrameOverflow   Code = "E_FRAME_OVERFLOW"  // frame count exceeds representable range

	// Segmentation.
	CodeMissingKeyframe Code = "E_MISSING_KEYFRAME" // no sync-sample index; samples must be scanned
	CodeMisalignedCut   Code = "E_MISALIGNED_CUT"   // representation cut deviates by more than one frame

	// Cache and checkpoint.
	CodeCacheTamper     Code = "E_CACHE_TAMPER"     // stored digest does not match payload; quarantined
	CodeCheckpointStale Code = "E_CHECKPOINT_STALE" // checkpoint digest does not match persisted segment

	// Streaming.
	CodeSinkClosed Code = "E_SINK_CLOSED" // consumer failed or was cancelled mid-stream
	CodeCancelled  Code = "E_CANCELLED"   // caller cancelled the operation
)

// Error carries a code, the location of the offending data and a recovery hint.
type Error struct {
	Code   Code
	Offset int64 // byte offset into the input, -1 when unknown
	Line   int   // 1-based line number, 0 when unknown
	Msg    string
	Hint   string // suggested recovery action
}

func (e *Error) Error() string {
	loc := ""
	if e.Line > 0 {
		loc = fmt.Sprintf(" line=%d", e.Line)
	}
	if e.Offset >= 0 {
		loc += fmt.Sprintf(" offset=%d", e.Offset)
	}
	return fmt.Sprintf("%s:%s %s (hint: %s)", e.Code, loc, e.Msg, e.Hint)
}

// New builds an Error with a location.
func New(code Code, line int, offset int64, msg, hint string) *Error {
	return &Error{Code: code, Line: line, Offset: offset, Msg: msg, Hint: hint}
}

// IsCode reports whether err is a *fault.Error with the given code.
func IsCode(err error, code Code) bool {
	fe, ok := err.(*Error)
	return ok && fe.Code == code
}
