package clock

import "fmt"

// Code is a stable, machine-readable error identifier shared by the parser,
// audio and manifest packages. Wire format: "<component>:<code>".
type Code string

const (
	// Generic / clock.
	CodeTimeBackwards Code = "time_backwards"
	CodePCRDiscont    Code = "pcr_discontinuity"
	CodeRTPDiscont    Code = "rtp_discontinuity"

	// RTP layer.
	CodeRTPSync      Code = "rtp_sync_error"
	CodeRTPDuplicate Code = "rtp_duplicate"
	CodeRTPLate      Code = "rtp_late_outside_window"
	CodeRTPLost      Code = "rtp_sequence_gap"

	// MPEG-TS layer.
	CodeTSSync       Code = "ts_sync_byte"
	CodeTSTruncated  Code = "ts_truncated_packet"
	CodeTSCCJump     Code = "ts_continuity_counter_jump"
	CodeTSPSI        Code = "ts_psi_error"
	CodeTSPES        Code = "ts_pes_error"
	CodeTSPCR        Code = "ts_pcr_invalid"
	CodeTSUnknownPID Code = "ts_unknown_pid"

	// Audio layer.
	CodeAudioFFmpeg Code = "audio_ffmpeg_failed"
	CodeAudioSpec   Code = "audio_spec_mismatch"

	// Pipeline / manifest.
	CodeCanceled      Code = "pipeline_canceled"
	CodeCheckpoint    Code = "checkpoint_invalid"
	CodeManifest      Code = "manifest_write_failed"
	CodeLimitExceeded Code = "resource_limit_exceeded"
)

// Error carries a stable code plus the source position where it was observed.
// All parser-level errors are *Error so the policy layer can classify them.
type Error struct {
	Code      Code   // stable identifier
	Source    string // producing component, e.g. "rtp", "ts:pid=0x100"
	Offset    int64  // byte/packet offset in the input (-1 if not applicable)
	FromNanos int64  // affected time range start (0 when unknown)
	ToNanos   int64  // affected time range end (0 when unknown)
	Message   string // human-readable detail
}

// New builds a clock.Error. Negative offsets and zero time ranges mean unknown.
func New(code Code, source string, offset, fromNanos, toNanos int64,
	format string, args ...any) *Error {
	return &Error{
		Code:      code,
		Source:    source,
		Offset:    offset,
		FromNanos: fromNanos,
		ToNanos:   toNanos,
		Message:   fmt.Sprintf(format, args...),
	}
}

// Error implements error.
func (e *Error) Error() string {
	pos := e.Source
	if e.Offset >= 0 {
		pos = fmt.Sprintf("%s@offset=%d", e.Source, e.Offset)
	}
	return fmt.Sprintf("%s: %s: %s", pos, e.Code, e.Message)
}

// Is supports errors.Is matching on the stable code.
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Code == e.Code
}

// AsError extracts a *clock.Error from err, or returns nil.
func AsError(err error) *Error {
	if err == nil {
		return nil
	}
	if e, ok := err.(*Error); ok {
		return e
	}
	return nil
}
