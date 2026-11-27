package model

import (
	"errors"
	"fmt"
)

// Code is a stable machine-readable error identifier. Codes never change once
// released; reports and tests rely on them rather than on message wording.
type Code string

const (
	// ErrUsage: bad command line parameters (exit status 1).
	ErrUsage Code = "ERR_USAGE"
	// ErrRTPTruncated: not enough bytes to form a legal RTP packet.
	ErrRTPTruncated Code = "ERR_RTP_TRUNCATED"
	// ErrRTPVersion: RTP version field is not 2.
	ErrRTPVersion Code = "ERR_RTP_VERSION"
	// ErrOpusTOC: TOC byte is missing or its config/code combination is invalid.
	ErrOpusTOC Code = "ERR_OPUS_TOC"
	// ErrAACHeader: AAC AU header is truncated or malformed.
	ErrAACHeader Code = "ERR_AAC_HEADER"
	// ErrMP4TimeBase: MP4 sample time-base boxes are missing or inconsistent.
	ErrMP4TimeBase Code = "ERR_MP4_TIMEBASE"
	// ErrSeqLate: packet sequence number is outside the reorder window.
	ErrSeqLate Code = "ERR_SEQ_LATE"
	// ErrGraphMissing: a dependency points at an unknown track or stage.
	ErrGraphMissing Code = "ERR_GRAPH_MISSING"
	// ErrGraphCycle: the dependency graph contains a cycle.
	ErrGraphCycle Code = "ERR_GRAPH_CYCLE"
	// ErrTimeOverflow: integer rational conversion overflows int64.
	ErrTimeOverflow Code = "ERR_TIME_OVERFLOW"
	// ErrPlanInfeasible: no candidate satisfies the quality/budget/size gates.
	ErrPlanInfeasible Code = "ERR_PLAN_INFEASIBLE"
	// ErrBudgetExceeded: stage CPU time or byte budget was exhausted.
	ErrBudgetExceeded Code = "ERR_BUDGET_EXCEEDED"
	// ErrStateCorrupt: persisted stage state cannot be read or matched.
	ErrStateCorrupt Code = "ERR_STATE_CORRUPT"
	// ErrManifestVerify: recomputed hashes/metrics do not match the manifest.
	ErrManifestVerify Code = "ERR_MANIFEST_VERIFY"
)

// Error carries a stable Code plus the exact source location of the failure so
// post-production can reconcile every rejected packet or stage without
// re-running the pipeline.
type Error struct {
	Code     Code   `json:"code"`
	Track    string `json:"track"`
	Sequence uint32 `json:"sequence,omitempty"`
	Stage    string `json:"stage,omitempty"`
	Offset   int64  `json:"offset,omitempty"`
	Message  string `json:"message"`
}

// At builds a located error from the code. Zero-valued location fields are
// omitted from the rendered message.
func (c Code) At(track string, seq uint32, stage string, offset int64, msg string) *Error {
	return &Error{Code: c, Track: track, Sequence: seq, Stage: stage, Offset: offset, Message: msg}
}

// Error implements the error interface with a deterministic single-line layout:
// CODE track=.. seq=.. stage=.. offset=..: message
func (e *Error) Error() string {
	s := string(e.Code)
	if e.Track != "" {
		s += " track=" + e.Track
	}
	if e.Sequence != 0 {
		s += fmt.Sprintf(" seq=%d", e.Sequence)
	}
	if e.Stage != "" {
		s += " stage=" + e.Stage
	}
	if e.Offset != 0 {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	return s + ": " + e.Message
}

// AsError extracts a *Error from any error, wrapping unknown errors as
// ERR_STATE_CORRUPT-class failures so callers always get a stable code.
func AsError(err error) *Error {
	var me *Error
	if errors.As(err, &me) {
		return me
	}
	return &Error{Code: ErrStateCorrupt, Message: err.Error()}
}

// ExitStatus maps an error to the documented process exit status:
// parameter errors -> 1, input/processing failures -> 2, success -> 0.
func ExitStatus(err error) int {
	if err == nil {
		return 0
	}
	me := AsError(err)
	if me.Code == ErrUsage {
		return 1
	}
	return 2
}
