// Package fault defines the single error model shared by every mediavault
// subsystem. Errors always carry a stable machine-readable code, the pipeline
// stage that observed them and, where meaningful, a byte offset.
package fault

import (
	"errors"
	"fmt"
)

// Stage identifies the pipeline phase in which an error was observed.
type Stage string

const (
	StageReceived      Stage = "RECEIVED"
	StageScanned       Stage = "SCANNED"
	StageFingerprinted Stage = "FINGERPRINTED"
	StageNormalized    Stage = "NORMALIZED"
	StageArchived      Stage = "ARCHIVED"
)

// Order returns a stable ordering index for a stage. Unknown stages sort last.
func (s Stage) Order() int {
	switch s {
	case StageReceived:
		return 0
	case StageScanned:
		return 1
	case StageFingerprinted:
		return 2
	case StageNormalized:
		return 3
	case StageArchived:
		return 4
	default:
		return 5
	}
}

// Error is the unified mediavault error. Offset is -1 when the failure cannot
// be attributed to a specific byte position.
type Error struct {
	Code   string
	Stage  Stage
	Offset int64
	Detail string
	Err    error
}

// New creates an error without an underlying cause.
func New(code string, stage Stage, offset int64, format string, args ...any) *Error {
	return &Error{Code: code, Stage: stage, Offset: offset, Detail: fmt.Sprintf(format, args...)}
}

// Wrap creates an error that retains an underlying cause.
func Wrap(code string, stage Stage, offset int64, err error, format string, args ...any) *Error {
	return &Error{Code: code, Stage: stage, Offset: offset, Detail: fmt.Sprintf(format, args...), Err: err}
}

func (e *Error) Error() string {
	msg := string(e.Stage) + ": " + e.Code
	if e.Offset >= 0 {
		msg += fmt.Sprintf(" at offset %d", e.Offset)
	}
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// CodeOf extracts the stable code of an error, or "" when it is not a fault.
func CodeOf(err error) string {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Code
	}
	return ""
}

// StageOf extracts the stage of an error, or "" when it is not a fault.
func StageOf(err error) Stage {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Stage
	}
	return ""
}

// OffsetOf extracts the byte offset, or -1.
func OffsetOf(err error) int64 {
	var fe *Error
	if errors.As(err, &fe) {
		return fe.Offset
	}
	return -1
}

// Stable error codes. These strings are part of the CLI contract.
const (
	CodeArgs          = "E_ARGS"
	CodeIO            = "E_IO"
	CodeRIFFLength    = "E_RIFF_LENGTH"
	CodeChunkBoundary = "E_CHUNK_BOUNDARY"
	CodeChunkSize     = "E_CHUNK_SIZE"
	CodeFrameAlign    = "E_FRAME_ALIGN"
	CodeFormat        = "E_WAV_FORMAT"
	CodeLimit         = "E_LIMIT"
	CodeDigest        = "E_DIGEST"
	CodeRTPSynth      = "E_RTP_SYNTH"
	CodeRTPDrift      = "E_RTP_DRIFT"
	CodeRTPBackward   = "E_RTP_BACKWARD"
	CodeRTPOverflow   = "E_RTP_OVERFLOW"
	CodeMP4Box        = "E_MP4_BOX"
	CodeMP4Extent     = "E_MP4_EXTENT"
	CodeCheckpoint    = "E_CHECKPOINT"
	CodeCommitted     = "E_COMMITTED_DIGEST"
	CodeManifest      = "E_MANIFEST"
	CodeVerify        = "E_VERIFY"
	CodePolicyVersion = "E_POLICY_VERSION"
)
