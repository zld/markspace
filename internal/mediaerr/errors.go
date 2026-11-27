// Package mediaerr defines the stable error model shared by every subsystem.
//
// Errors carry a stable string code (so downstream tooling can rely on them),
// the PID and input byte offset they relate to (zero when not applicable) and
// a human-readable message plus recovery action.
package mediaerr

import "fmt"

// Stable error codes. The string values are part of the public contract and
// must not be renamed; new codes may be appended.
const (
	// TS packet layer.
	CodeSyncLost           = "TS_SYNC_LOST"
	CodeTruncatedPacket    = "TS_TRUNCATED_PACKET"
	CodeAdaptationOverflow = "TS_ADAPTATION_OVERFLOW"
	CodeCCGap              = "TS_CC_GAP"
	CodeCCRepeat           = "TS_CC_REPEAT"
	CodeCCJump             = "TS_CC_JUMP"

	// PSI / PAT / PMT.
	CodeSectionTooLong  = "PSI_SECTION_TOO_LONG"
	CodeSectionMissing  = "PSI_SECTION_MISSING"
	CodeCRCError        = "PSI_CRC_ERROR"
	CodeVersionRollback = "PSI_VERSION_ROLLBACK"
	CodeNoMapping       = "PSI_NO_MAPPING"

	// Clocks.
	CodePCRWrap           = "PCR_WRAP"
	CodePCRDiscontinuity  = "PCR_DISCONTINUITY"
	CodePCRDriftClamped   = "PCR_DRIFT_CLAMPED"
	CodePCROutlier        = "PCR_OUTLIER"
	CodeMissingPCRAnchor  = "PCR_MISSING_ANCHOR"
	CodeTimeOverflow      = "TIME_OVERFLOW"
	CodePTSWrap           = "PTS_WRAP"
	CodePTSBackward       = "PTS_BACKWARD"
	CodePTSDuplicate      = "PTS_DUPLICATE"
	CodePTSNoAnchor       = "PTS_NO_ANCHOR"
	CodeSampleUnindexable = "SAMPLE_UNINDEXABLE"

	// RTP transport.
	CodeRTPSeqGap    = "RTP_SEQ_GAP"
	CodeRTPSeqRepeat = "RTP_SEQ_REPEAT"
	CodeRTPSeqJump   = "RTP_SEQ_JUMP"

	// Segment / fan-in layer.
	CodeBoundaryMismatch     = "BOUNDARY_MISMATCH"
	CodeRepresentationFailed = "REPRESENTATION_FAILED"
	CodeRoundtripField       = "ROUNDTRIP_FIELD_MISMATCH"
)

// Severity classifies whether processing can continue after the error.
type Severity string

const (
	// SeverityRecoverable: the stream remains usable, a local repair strategy
	// was applied.
	SeverityRecoverable Severity = "recoverable"
	// SeverityFatal: structural damage that makes the affected scope
	// (representation or whole stream) unrepairable.
	SeverityFatal Severity = "fatal"
)

// Error is the structured error type used throughout the pipeline.
type Error struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	PID      uint16   `json:"pid,omitempty"`
	Offset   int64    `json:"offset,omitempty"`
	Message  string   `json:"message"`
	Action   string   `json:"action"`
}

func (e *Error) Error() string {
	loc := ""
	if e.PID != 0 || e.Offset != 0 {
		loc = fmt.Sprintf(" pid=0x%04x offset=%d", e.PID, e.Offset)
	}
	return fmt.Sprintf("%s [%s]%s: %s (action: %s)", e.Code, e.Severity, loc, e.Message, e.Action)
}

// New builds a structured error.
func New(code string, sev Severity, pid uint16, offset int64, msg, action string) *Error {
	return &Error{Code: code, Severity: sev, PID: pid, Offset: offset, Message: msg, Action: action}
}

// Recoverable builds a recoverable error.
func Recoverable(code string, pid uint16, offset int64, msg, action string) *Error {
	return New(code, SeverityRecoverable, pid, offset, msg, action)
}

// Fatal builds a structural, unrecoverable error.
func Fatal(code string, pid uint16, offset int64, msg, action string) *Error {
	return New(code, SeverityFatal, pid, offset, msg, action)
}

// Collector accumulates structured errors while processing a stream.
// It is safe for single-goroutine streaming use; representations use separate
// collectors.
type Collector struct {
	errs []*Error
}

// Add records an error and returns it, so callers can return it if desired.
func (c *Collector) Add(e *Error) *Error {
	c.errs = append(c.errs, e)
	return e
}

// All returns the recorded errors in occurrence order.
func (c *Collector) All() []*Error { return c.errs }

// HasFatal reports whether any fatal error was recorded.
func (c *Collector) HasFatal() bool {
	for _, e := range c.errs {
		if e.Severity == SeverityFatal {
			return true
		}
	}
	return false
}

// CountByCode returns the number of recorded errors with the given code.
func (c *Collector) CountByCode(code string) int {
	n := 0
	for _, e := range c.errs {
		if e.Code == code {
			n++
		}
	}
	return n
}
