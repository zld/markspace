// Package fault provides the common error and failure manifest model. Every
// pipeline stage preserves the source byte offset and track identifier so a
// bad record can be isolated, reported, and resumed from without guessing.
package fault

import (
	"errors"
	"fmt"
)

// Stage identifies a pipeline phase.
type Stage string

const (
	StageParse    Stage = "parse"
	StageRTP      Stage = "rtp"
	StageIndex    Stage = "index"
	StageTimeline Stage = "timeline"
	StageBudget   Stage = "budget"
	StageRender   Stage = "render"
	StageVerify   Stage = "verify"
)

// Failure is the serializable, stable manifest entry used by CLI output and
// resume checkpoints.
type Failure struct {
	Stage     Stage  `json:"stage"`
	Code      string `json:"code"`
	Offset    int64  `json:"offset"`
	TrackID   int    `json:"track_id"`
	Message   string `json:"message"`
	Recovered bool   `json:"recovered,omitempty"`
}

// Error is an error carrying the original input location. Unknown numeric
// coordinates use -1 rather than 0 because offset zero is a valid location.
type Error struct {
	Stage   Stage
	Code    string
	Offset  int64
	TrackID int
	Err     error
}

// New constructs an offset-aware error.
func New(stage Stage, code string, offset int64, trackID int, err error) *Error {
	return &Error{Stage: stage, Code: code, Offset: offset, TrackID: trackID, Err: err}
}

// Error implements error.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	detail := ""
	if e.Err != nil {
		detail = ": " + e.Err.Error()
	}
	return fmt.Sprintf("%s failure code=%s offset=%d track=%d%s", e.Stage, e.Code, e.Offset, e.TrackID, detail)
}

// Unwrap exposes the wrapped cause.
func (e *Error) Unwrap() error { return e.Err }

// As converts an error to a Failure when it carries fault metadata. The bool
// result is false for ordinary errors.
func As(err error) (Failure, bool) {
	var fe *Error
	if !errors.As(err, &fe) || fe == nil {
		return Failure{}, false
	}
	message := fe.Error()
	if fe.Err != nil {
		message = fe.Err.Error()
	}
	return Failure{
		Stage:   fe.Stage,
		Code:    fe.Code,
		Offset:  fe.Offset,
		TrackID: fe.TrackID,
		Message: message,
	}, true
}

// Failures converts all offset-aware errors in errs.
func Failures(errs []error) []Failure {
	out := make([]Failure, 0, len(errs))
	for _, err := range errs {
		if f, ok := As(err); ok {
			out = append(out, f)
		}
	}
	return out
}
