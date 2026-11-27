package model

import "fmt"

// Stage identifiers attached to every structured error.
const (
	StageWAV       = "wav"
	StageRTP       = "rtp"
	StageUnwrap    = "unwrap"
	StageClock     = "clock"
	StageSegment   = "segment"
	StageTransform = "transform"
	StageValidate  = "validate"
)

// Reason codes classify pipeline anomalies. They are stable strings so
// regression fixtures can assert on them.
const (
	ReasonHeader         = "bad_header"
	ReasonLimit          = "limit_exceeded"
	ReasonTruncatedBlock = "truncated_block"
	ReasonDuplicate      = "duplicate_packet"
	ReasonLate           = "late_packet"
	ReasonSeqGap         = "sequence_gap"
	ReasonBackwards      = "timestamp_backwards"
	ReasonJump           = "timestamp_jump"
	ReasonIllegalWrap    = "illegal_wrap"
	ReasonDrift          = "drift_exceeded"
	ReasonConstraint     = "constraint_failed"
)

// Anomaly is one structured finding. Offset is the raw input byte offset
// when one exists, otherwise -1. RawValue preserves the offending input
// value (e.g. the original RTP timestamp) so anomalies never rewrite
// inputs.
type Anomaly struct {
	Stage    string `json:"stage"`
	Reason   string `json:"reason"`
	Offset   int64  `json:"offset"`
	RawValue int64  `json:"raw_value"`
	Message  string `json:"message"`
	Recovery string `json:"recovery"`
}

// String renders a compact single-line form for terminal diagnostics.
func (a Anomaly) String() string {
	return fmt.Sprintf("%s/%s@%d: %s (recovery: %s)",
		a.Stage, a.Reason, a.Offset, a.Message, a.Recovery)
}

// StructuredError carries machine-readable findings alongside the plain
// error interface. Fatal parse failures use it with exit code 1;
// recoverable anomalies are collected instead in the parse result.
type StructuredError struct {
	Anomaly Anomaly
	Cause   error
}

// Error implements error.
func (e *StructuredError) Error() string {
	if e.Cause != nil {
		return e.Anomaly.String() + ": " + e.Cause.Error()
	}
	return e.Anomaly.String()
}

// Unwrap exposes the wrapped cause to errors.Is/errors.As.
func (e *StructuredError) Unwrap() error { return e.Cause }

// NewStructuredError builds a fatal structured error.
func NewStructuredError(stage, reason string, offset int64, raw int64, msg, recovery string, cause error) *StructuredError {
	return &StructuredError{
		Anomaly: Anomaly{
			Stage: stage, Reason: reason, Offset: offset,
			RawValue: raw, Message: msg, Recovery: recovery,
		},
		Cause: cause,
	}
}

// Fatal reports whether a reason makes further parsing impossible.
func Fatal(reason string) bool {
	return reason == ReasonHeader || reason == ReasonLimit
}
