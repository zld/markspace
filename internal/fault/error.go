// Package fault defines the structured error model shared by every
// subsystem. Every failure carries the pipeline stage, the codec involved,
// a stable error code and, where meaningful, the byte offset at which it was
// observed. Callers must inspect errors via Code rather than string matching.
package fault

import (
	"errors"
	"fmt"
)

// Stage identifies the pipeline stage that produced an error.
type Stage string

// Pipeline stages in processing order.
const (
	StageScan     Stage = "scan"
	StagePlan     Stage = "plan"
	StageWorker   Stage = "worker"
	StagePublish  Stage = "publish"
	StageRecovery Stage = "recovery"
	StageVerify   Stage = "verify"
)

// Code is a stable, machine-readable failure identifier.
type Code string

// Error codes. The set is closed: new failure modes must add a constant here.
const (
	CodeIndexOutOfRange      Code = "index_offset_out_of_range"
	CodeIndexNotKeyframe     Code = "index_points_to_non_keyframe"
	CodeIndexTimeDuplicate   Code = "index_timestamp_duplicate"
	CodeTruncatedTail        Code = "truncated_elementary_stream_tail"
	CodeParse                Code = "bitstream_parse_error"
	CodeBudgetUnsatisfiable  Code = "no_candidate_satisfies_budget"
	CodeQualityBelowMin      Code = "candidate_ssim_below_minimum"
	CodeBitrateAboveMax      Code = "candidate_bitrate_above_maximum"
	CodeWorkOverBudget       Code = "candidate_work_over_budget"
	CodeSubprocessFailed     Code = "subprocess_failed"
	CodeSubprocessTimeout    Code = "subprocess_timeout"
	CodeDependencyMissing    Code = "local_dependency_missing"
	CodeHashMismatch         Code = "segment_hash_mismatch"
	CodeStateConflict        Code = "journal_state_conflict"
	CodeContentConflict      Code = "content_conflict_refused_overwrite"
	CodeManifestInconsistent Code = "manifest_inconsistent"
	CodeBoundaryDiscontinuity Code = "segment_boundary_discontinuity"
	CodeIO                   Code = "io_error"
)

// Error is the structured error type used across the pipeline.
type Error struct {
	Stage  Stage
	Code   Code
	Codec  string
	Offset int64 // -1 when not applicable
	Detail string
	Err    error
}

// New builds a structured error with no byte offset.
func New(stage Stage, code Code, codec, detail string) *Error {
	return &Error{Stage: stage, Code: code, Codec: codec, Offset: -1, Detail: detail}
}

// At builds a structured error anchored at a byte offset.
func At(stage Stage, code Code, codec string, offset int64, detail string) *Error {
	return &Error{Stage: stage, Code: code, Codec: codec, Offset: offset, Detail: detail}
}

// Wrap attaches a causal error.
func (e *Error) Wrap(cause error) *Error {
	e.Err = cause
	return e
}

// Error implements error.
func (e *Error) Error() string {
	s := string(e.Stage) + "/" + string(e.Code)
	if e.Codec != "" {
		s += " (" + e.Codec + ")"
	}
	if e.Offset >= 0 {
		s += fmt.Sprintf(" @offset=%d", e.Offset)
	}
	if e.Detail != "" {
		s += ": " + e.Detail
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap supports errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Err }

// AsError extracts a *fault.Error from any error chain.
func AsError(err error) (*Error, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

// HasCode reports whether err (or any error in its chain) carries code.
func HasCode(err error, code Code) bool {
	fe, ok := AsError(err)
	if !ok {
		return false
	}
	if fe.Code == code {
		return true
	}
	return fe.Err != nil && HasCode(fe.Err, code)
}

// Join aggregates structured errors, for example one per corrupted index
// entry, while preserving the chain for errors.As/errors.Is.
type Join struct {
	Errs []error
}

// Aggregate returns nil if errs is empty, a single error if it has one
// element, otherwise a *Join preserving every failure.
func Aggregate(errs []error) error {
	filtered := make([]error, 0, len(errs))
	for _, e := range errs {
		if e != nil {
			filtered = append(filtered, e)
		}
	}
	switch len(filtered) {
	case 0:
		return nil
	case 1:
		return filtered[0]
	default:
		return &Join{Errs: filtered}
	}
}

func (j *Join) Error() string {
	return fmt.Sprintf("%d structured error(s); first: %v", len(j.Errs), j.Errs[0])
}

// Unwrap returns the wrapped errors for errors.Is/errors.As support.
func (j *Join) Unwrap() []error { return j.Errs }

// Codes returns the distinct codes carried by an error chain. A plain error
// yields an empty slice.
func Codes(err error) []Code {
	var seen []Code
	var walk func(error)
	walk = func(e error) {
		var j *Join
		if errors.As(e, &j) {
			for _, sub := range j.Errs {
				walk(sub)
			}
			return
		}
		var fe *Error
		if errors.As(e, &fe) {
			seen = append(seen, fe.Code)
			if fe.Err != nil {
				walk(fe.Err)
			}
		}
	}
	walk(err)
	return seen
}
