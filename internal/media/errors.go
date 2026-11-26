package media

import (
	"errors"
	"fmt"
)

// Code identifies a stable rejection category.
type Code string

const (
	CodeInvalidArgument  Code = "invalid_argument"
	CodePathEscape       Code = "path_escape"
	CodeManifest         Code = "manifest_error"
	CodeDuplicateSeq     Code = "duplicate_sequence"
	CodeDimensions       Code = "invalid_dimensions"
	CodeFormat           Code = "unknown_format"
	CodeChecksum         Code = "checksum_mismatch"
	CodeDecode           Code = "decode_failed"
	CodeRTPAmbiguous     Code = "rtp_wrap_ambiguous"
	CodeMP4Truncated     Code = "mp4_truncated"
	CodeOffsetOutOfRange Code = "sample_offset_out_of_range"
	CodeInputChanged     Code = "input_changed_during_resume"
	CodeCheckpoint       Code = "checkpoint_corrupt"
	CodeVerification     Code = "verification_failed"
	CodeWrite            Code = "write_failed"
	CodeInterrupt        Code = "interrupted"
)

// Error is the common error model used across subsystems.
//
// Offset is negative when a byte offset is not knowable for the failure.
type Error struct {
	Code   Code   `json:"code"`
	Stage  string `json:"stage"`
	File   string `json:"file,omitempty"`
	Frame  int    `json:"frame,omitempty"`
	Offset int64  `json:"offset"`
	Err    error  `json:"-"`
}

func E(code Code, stage string, err error) *Error {
	return &Error{Code: code, Stage: stage, Offset: -1, Err: err}
}

func (e *Error) WithFile(file string) *Error { e.File = file; return e }
func (e *Error) WithFrame(frame int) *Error  { e.Frame = frame; return e }
func (e *Error) WithOffset(offset int64) *Error {
	e.Offset = offset
	return e
}

func (e *Error) Error() string {
	msg := string(e.Code) + " at " + e.Stage
	if e.File != "" {
		msg += " file=" + e.File
	}
	if e.Frame > 0 {
		msg += fmt.Sprintf(" frame=%d", e.Frame)
	}
	if e.Offset >= 0 {
		msg += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

func AsError(err error) *Error {
	var me *Error
	if errors.As(err, &me) {
		return me
	}
	return nil
}

func HasCode(err error, code Code) bool {
	me := AsError(err)
	return me != nil && me.Code == code
}
