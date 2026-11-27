// Package model defines the time basis, error model and shared media
// structures used by the demux, timeline, remux and report subsystems.
package model

import "errors"

// ErrorCode is a stable, machine-readable identifier for a failure class.
// Numeric values are part of the contract and must not be reused.
type ErrorCode int

const (
	// CodeOK is the absence of an error.
	CodeOK ErrorCode = 0
	// CodeTruncatedBox: a box/KLV run ends before its declared length.
	CodeTruncatedBox ErrorCode = 1001
	// CodeOutOfRangeSample: a sample table points outside the file or past mdat.
	CodeOutOfRangeSample ErrorCode = 1002
	// CodeMalformedContainer: structural rules other than truncation are violated.
	CodeMalformedContainer ErrorCode = 1003
	// CodeMissingTrack: an explicit mapping references a source track that is absent.
	CodeMissingTrack ErrorCode = 2001
	// CodeDuplicateTarget: two source tracks map onto the same target track.
	CodeDuplicateTarget ErrorCode = 2002
	// CodeIncompatibleCodec: mapped tracks cannot share a target container/role.
	CodeIncompatibleCodec ErrorCode = 2003
	// CodeLateBoundary: an event arrived after the watermark had passed it.
	CodeLateBoundary ErrorCode = 3001
	// CodeClockWrap: a clock wrapped backwards and required clock expansion.
	CodeClockWrap ErrorCode = 3002
	// CodeOverlap: source-switch overlap cannot be resolved by the configured rule.
	CodeOverlap ErrorCode = 3003
	// CodeGap: a source-switch gap violates the configured tolerance.
	CodeGap ErrorCode = 3004
	// CodeVerify: output verification (byte equality, manifest) failed.
	CodeVerify ErrorCode = 4001
)

// MediaError carries every datum needed to locate a failure without
// re-reading logs: byte offset, track, event time and a stable code.
type MediaError struct {
	Code     ErrorCode `json:"code"`
	Message  string    `json:"message"`
	File     string    `json:"file,omitempty"`
	Offset   int64     `json:"offset,omitempty"`
	Track    uint32    `json:"track,omitempty"`
	TimeTick int64     `json:"timeTick,omitempty"`
	EventSeq uint64    `json:"eventSeq,omitempty"`
}

func (e *MediaError) Error() string {
	s := e.Message
	if e.File != "" {
		s += " file=" + e.File
	}
	if e.Offset >= 0 {
		s += " offset=" + itoa(e.Offset)
	}
	if e.Track != 0 {
		s += " track=" + itoa(int64(e.Track))
	}
	if e.TimeTick != 0 {
		s += " tick=" + itoa(e.TimeTick)
	}
	return s
}

// NewError builds a MediaError. Offset defaults to -1 ("unknown").
func NewError(code ErrorCode, msg string) *MediaError {
	return &MediaError{Code: code, Message: msg, Offset: -1}
}

// WithFile annotates and returns the receiver for fluent construction.
func (e *MediaError) WithFile(name string) *MediaError { e.File = name; return e }

// WithOffset annotates and returns the receiver.
func (e *MediaError) WithOffset(off int64) *MediaError { e.Offset = off; return e }

// WithTrack annotates and returns the receiver.
func (e *MediaError) WithTrack(t uint32) *MediaError { e.Track = t; return e }

// WithTick annotates and returns the receiver.
func (e *MediaError) WithTick(t int64) *MediaError { e.TimeTick = t; return e }

// WithEvent annotates and returns the receiver.
func (e *MediaError) WithEvent(seq uint64) *MediaError { e.EventSeq = seq; return e }

// AsMediaError unwraps a MediaError from any wrapped error.
func AsMediaError(err error) (*MediaError, bool) {
	var me *MediaError
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// CodeOf extracts the stable code from an error, or CodeOK for nil.
func CodeOf(err error) ErrorCode {
	if me, ok := AsMediaError(err); ok {
		return me.Code
	}
	if err != nil {
		return CodeMalformedContainer
	}
	return CodeOK
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
