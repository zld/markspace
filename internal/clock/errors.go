package clock

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ErrorCode is a stable, machine-readable classification for pipeline failures.
type ErrorCode string

const (
	CodeInvalidPacket          ErrorCode = "INVALID_PACKET"
	CodePacketTooLarge         ErrorCode = "PACKET_TOO_LARGE"
	CodeMissingField           ErrorCode = "MISSING_FIELD"
	CodeUnsupportedPayload     ErrorCode = "UNSUPPORTED_PAYLOAD"
	CodeCorruptPayload         ErrorCode = "CORRUPT_PAYLOAD"
	CodeGap                    ErrorCode = "RTP_SEQUENCE_GAP"
	CodeReorder                ErrorCode = "RTP_REORDER"
	CodeMissingClockReport     ErrorCode = "MISSING_RTCP_REPORT"
	CodeClockRollback          ErrorCode = "CLOCK_REPORT_ROLLBACK"
	CodeUnsupportedParserMajor ErrorCode = "UNSUPPORTED_PARSER_MAJOR"
	CodeInputDigestMismatch    ErrorCode = "INPUT_DIGEST_MISMATCH"
	CodeParserMajorMismatch    ErrorCode = "PARSER_MAJOR_MISMATCH"
	CodeCheckpointInvalid      ErrorCode = "CHECKPOINT_INVALID"
	CodeQueueTooSmall          ErrorCode = "QUEUE_LIMIT_TOO_SMALL"
)

// Location identifies an input position. ByteOffset is zero-based in the
// project-generated source stream; OffsetError records the guaranteed mapping
// tolerance when the original exact position is not directly available.
type Location struct {
	SSRC             uint32 `json:"ssrc,omitempty"`
	Sequence         uint16 `json:"sequence,omitempty"`
	ExtendedSequence uint64 `json:"extended_sequence,omitempty"`
	Timestamp        uint32 `json:"rtp_timestamp,omitempty"`
	ByteOffset       int64  `json:"byte_offset"`
	PacketByteOffset int64  `json:"packet_byte_offset,omitempty"`
	OffsetError      int64  `json:"offset_error_bytes,omitempty"`
	PacketError      int64  `json:"offset_error_packets,omitempty"`
	ParserMajor      int    `json:"parser_major,omitempty"`
}

// Error is the only error type allowed to cross subsystem boundaries. Adapters
// convert their own parser errors into this type so implementation details do
// not perturb the shared timeline.
type Error struct {
	Code     ErrorCode `json:"error_code"`
	Message  string    `json:"message"`
	Location *Location `json:"location,omitempty"`
	Err      error     `json:"-"`
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Location != nil {
		b, _ := json.Marshal(e.Location)
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, string(b))
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

func NewError(code ErrorCode, message string, loc *Location, cause error) *Error {
	return &Error{Code: code, Message: message, Location: cloneLocation(loc), Err: cause}
}

func Errorf(code ErrorCode, loc *Location, format string, args ...any) *Error {
	return NewError(code, fmt.Sprintf(format, args...), loc, nil)
}

func Wrap(code ErrorCode, loc *Location, cause error, format string, args ...any) *Error {
	return NewError(code, fmt.Sprintf(format+": %v", append(args, cause)...), loc, cause)
}

func cloneLocation(loc *Location) *Location {
	if loc == nil {
		return nil
	}
	cp := *loc
	return &cp
}

// AsError finds a clock.Error in err's wrap chain.
func AsError(err error) (*Error, bool) {
	var ce *Error
	if errors.As(err, &ce) {
		return ce, true
	}
	return nil, false
}

// CodeOf returns the stable code for an error, or INVALID_PACKET as a safe
// boundary classification for ordinary unexpected errors.
func CodeOf(err error) ErrorCode {
	if ce, ok := AsError(err); ok {
		return ce.Code
	}
	return CodeInvalidPacket
}
