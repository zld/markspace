package media

import (
	"errors"
	"fmt"
)

// Sentinel errors wrap the stable error codes so callers can use
// errors.Is while records keep their machine-readable ErrorCode.
var (
	ErrSentinelConflict   = errors.New(string(ErrConflict))
	ErrSentinelCheckpoint = errors.New(string(ErrCheckpoint))
)

// ErrorCode is the stable, machine-readable classification of an ingest
// or processing failure. Codes never change meaning once released.
type ErrorCode string

const (
	// ErrPacketTooLarge: a packet/sample exceeds the 1 MiB limit.
	ErrPacketTooLarge ErrorCode = "PACKET_TOO_LARGE"
	// ErrRTPParse: the RTP header/payload cannot be parsed.
	ErrRTPParse ErrorCode = "RTP_PARSE_ERROR"
	// ErrExtendedSeq: sequence-number bookkeeping detected an
	// unrecoverable ordering failure.
	ErrExtendedSeq ErrorCode = "SEQUENCE_ERROR"
	// ErrCorruptPacket: payload is damaged and its duration cannot be
	// determined; the affected interval must stay HOLD, never guessed.
	ErrCorruptPacket ErrorCode = "CORRUPT_PACKET"
	// ErrUnknownDuration: packet reached the pipeline without enough
	// information to derive its timeline extent.
	ErrUnknownDuration ErrorCode = "UNKNOWN_DURATION"
	// ErrIndexIncomplete: an fMP4 index (trun/tfdt/moof) is missing or
	// truncated, so the sample extent cannot be located.
	ErrIndexIncomplete ErrorCode = "INDEX_INCOMPLETE"
	// ErrUnsupportedCodec: codec is not Opus or AAC-LC.
	ErrUnsupportedCodec ErrorCode = "UNSUPPORTED_CODEC"
	// ErrUnsupportedLayout: channel count is neither 1 nor 2.
	ErrUnsupportedLayout ErrorCode = "UNSUPPORTED_LAYOUT"
	// ErrDecode: the external decoder rejected the payload.
	ErrDecode ErrorCode = "DECODE_ERROR"
	// ErrCheckpoint: checkpoint failed hash/version validation.
	ErrCheckpoint ErrorCode = "CHECKPOINT_ERROR"
	// ErrConflict: same stable task key, different input content.
	ErrConflict ErrorCode = "INPUT_CONFLICT"
	// ErrBudget: output samples do not match the declared target.
	ErrBudget ErrorCode = "BUDGET_DEVIATION"
	// ErrPathRejected: traversal or symbolic-link input.
	ErrPathRejected ErrorCode = "PATH_REJECTED"
)

// MediaError is the structured error record attached to every timeline
// problem. ByteOffset and Seq identify the offending packet when known;
// When is the timeline interval (zero Start/End means "not locatable").
type MediaError struct {
	Code       ErrorCode `json:"code"`
	Message    string    `json:"message"`
	Seq        uint64    `json:"seq,omitempty"`
	ByteOffset int64     `json:"byte_offset,omitempty"`
	When       Range     `json:"when"`
	Locatable  bool      `json:"locatable"`
}

// Error implements the error interface.
func (e *MediaError) Error() string {
	loc := "HOLD"
	if e.Locatable {
		loc = fmt.Sprintf("[%d,%d)", e.When.Start, e.When.End)
	}
	return fmt.Sprintf("%s: %s (seq=%d offset=%d interval=%s)",
		e.Code, e.Message, e.Seq, e.ByteOffset, loc)
}

// NewError builds a locatable error record bound to a timeline interval.
func NewError(code ErrorCode, msg string, seq uint64, offset int64, when Range) *MediaError {
	return &MediaError{Code: code, Message: msg, Seq: seq, ByteOffset: offset, When: when, Locatable: true}
}

// NewPendingError builds a record for an interval that cannot be located
// on the timeline; such intervals remain HOLD and are never guessed.
func NewPendingError(code ErrorCode, msg string, seq uint64, offset int64) *MediaError {
	return &MediaError{Code: code, Message: msg, Seq: seq, ByteOffset: offset, Locatable: false}
}

// ErrorLog is the append-only, deduplicated collection of error records
// for one task. Records are kept in insertion order.
type ErrorLog struct {
	items []*MediaError
	seen  map[string]struct{}
}

// NewErrorLog creates an empty error log.
func NewErrorLog() *ErrorLog {
	return &ErrorLog{seen: map[string]struct{}{}}
}

// Add appends an error unless an identical record already exists.
func (l *ErrorLog) Add(e *MediaError) {
	if e == nil {
		return
	}
	key := e.Error()
	if _, ok := l.seen[key]; ok {
		return
	}
	l.seen[key] = struct{}{}
	l.items = append(l.items, e)
}

// All returns the recorded errors in insertion order.
func (l *ErrorLog) All() []*MediaError {
	if l == nil {
		return nil
	}
	out := make([]*MediaError, len(l.items))
	copy(out, l.items)
	return out
}

// Has reports whether any record of the given code exists.
func (l *ErrorLog) Has(code ErrorCode) bool {
	for _, e := range l.items {
		if e.Code == code {
			return true
		}
	}
	return false
}

// Len returns the number of distinct records.
func (l *ErrorLog) Len() int { return len(l.items) }
