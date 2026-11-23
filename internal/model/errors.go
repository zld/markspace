// Package model defines the unified forensic timeline model and the error
// type that carries input byte offsets from parsing all the way to the
// validator and manifest.
package model

import "fmt"

// Processing stages used in ForensicError.Stage and in stage manifests.
const (
	StageParseBox   = "parse_box"
	StageIndex      = "index_rebuild"
	StageRepair     = "repair"
	StageClock      = "clock"
	StageRTPRecover = "rtp_recover"
	StageRender     = "render"
	StageValidate   = "validate"
	StageManifest   = "manifest"
)

// Confidence grades attached to rebuilt samples and intervals.
const (
	// ConfidenceHigh: every property comes from an intact source table.
	ConfidenceHigh = "high"
	// ConfidenceMedium: interval rebuilt from structural boundaries
	// (sample size table + chunk geometry) after an index defect.
	ConfidenceMedium = "medium"
	// ConfidenceLow: inferred position only; adjacent to a damaged region.
	ConfidenceLow = "low"
	// ConfidenceNone: the interval is excluded from the valid timeline.
	ConfidenceNone = "none"
)

// ForensicError is the single error type propagated between subsystems. It
// always carries the stage that detected the problem and the original input
// byte offset so that any anomalous output frame can be traced back to bytes.
type ForensicError struct {
	Stage    string `json:"stage"`
	Offset   int64  `json:"offset"`
	TrackID  int    `json:"track_id"`
	Phase    string `json:"phase"`
	Message  string `json:"message"`
	Terminal bool   `json:"terminal"`
}

// Error implements error.
func (e *ForensicError) Error() string {
	return fmt.Sprintf("forensic[%s] track=%d phase=%s offset=%d: %s",
		e.Stage, e.TrackID, e.Phase, e.Offset, e.Message)
}

// NewError constructs a ForensicError. Offset < 0 means "no concrete byte
// offset is known at this layer" and is rendered as -1 in manifests.
func NewError(stage, phase string, trackID int, offset int64, message string) *ForensicError {
	return &ForensicError{
		Stage:   stage,
		Phase:   phase,
		TrackID: trackID,
		Offset:  offset,
		Message: message,
	}
}

// AsForensicError extracts a *ForensicError from any error, wrapping plain
// errors in a generic forensic record.
func AsForensicError(err error) *ForensicError {
	if err == nil {
		return nil
	}
	if fe, ok := err.(*ForensicError); ok {
		return fe
	}
	return &ForensicError{Stage: "unknown", Phase: "propagate", TrackID: -1,
		Offset: -1, Message: err.Error()}
}
