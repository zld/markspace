// Package ingest parses length-framed RTP/RTCP byte streams. Every parse
// failure is reported with the raw stream offset, the pipeline stage and a
// stable error code so corruption can be located to the byte.
package ingest

import "fmt"

// Stage tags identify where inside the pipeline a failure was detected.
const (
	StageParse = "parse"
)

// ErrorCode is the machine-readable taxonomy of ingest failures.
type ErrorCode string

const (
	ErrTruncated       ErrorCode = "TRUNCATED"         // stream ended mid-frame
	ErrBadLength       ErrorCode = "BAD_LENGTH"        // length field is unusable
	ErrPayloadTooLarge ErrorCode = "PAYLOAD_TOO_LARGE" // frame exceeds 64 KiB
	ErrRTPHeader       ErrorCode = "RTP_HEADER"        // RTP fixed header invalid
	ErrRTCPHeader      ErrorCode = "RTCP_HEADER"       // RTCP compound packet invalid
	ErrUnknownFrame    ErrorCode = "UNKNOWN_FRAME"     // frame type tag unknown
)

// MaxFramePayload is the hard per-packet limit (64 KiB).
const MaxFramePayload = 64 * 1024

// ParseError carries the triple required by the spec: code, offset, stage.
type ParseError struct {
	Code   ErrorCode
	Offset int64 // byte offset of the offending frame in the raw stream
	Stage  string
	Err    error
}

func (e *ParseError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s at offset %d stage %s: %v", e.Code, e.Offset, e.Stage, e.Err)
	}
	return fmt.Sprintf("%s at offset %d stage %s", e.Code, e.Offset, e.Stage)
}

// Unwrap exposes the underlying cause for errors.Is/As.
func (e *ParseError) Unwrap() error { return e.Err }
