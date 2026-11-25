package model

import (
	"errors"
	"fmt"
)

// Stage is one of the ordered pipeline phases. Recovery always resumes at the
// stage after LastOK.
type Stage string

const (
	StageIngest    Stage = "ingest"
	StageNormalize Stage = "normalize"
	StageSplit     Stage = "split"
	StageDerive    Stage = "derive"
	StageManifest  Stage = "manifest"
	StageComplete  Stage = "complete"
)

// StageOrder is the canonical phase sequence.
var StageOrder = []Stage{
	StageIngest, StageNormalize, StageSplit, StageDerive, StageManifest, StageComplete,
}

// Index returns the position of s in StageOrder, or -1.
func (s Stage) Index() int {
	for i, v := range StageOrder {
		if v == s {
			return i
		}
	}
	return -1
}

// Next returns the following stage, or StageComplete at the end.
func (s Stage) Next() Stage {
	i := s.Index()
	if i < 0 || i+1 >= len(StageOrder) {
		return StageComplete
	}
	return StageOrder[i+1]
}

// Error is a pipeline failure carrying the phase, the track (or event
// source), and the byte offset in the original input that made the defect
// locatable. Offset is never negative.
type Error struct {
	Stage   Stage    `json:"stage"`
	Op      string   `json:"op"`
	Track   TrackRef `json:"track"`
	Offset  uint64   `json:"offset"`
	Message string   `json:"message"`
	Cause   error    `json:"-"`
}

// TrackRef identifies a container track, an RTP stream, or a derived fixture.
type TrackRef struct {
	ID   uint32 `json:"id"`
	Kind string `json:"kind,omitempty"`
	Name string `json:"name,omitempty"`
}

func (t TrackRef) IsZero() bool { return t.ID == 0 && t.Kind == "" && t.Name == "" }

func (t TrackRef) String() string {
	if t.Name != "" {
		return t.Name
	}
	if t.Kind != "" {
		return fmt.Sprintf("%s:%d", t.Kind, t.ID)
	}
	if t.ID != 0 {
		return fmt.Sprintf("track:%d", t.ID)
	}
	return "input"
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	cause := ""
	if e.Cause != nil {
		cause = ": " + e.Cause.Error()
	}
	return fmt.Sprintf("%s/%s track=%s offset=%d: %s%s",
		e.Stage, e.Op, e.Track.String(), e.Offset, e.Message, cause)
}

func (e *Error) Unwrap() error { return e.Cause }

// NewError builds a stage/track/offset error.
func NewError(stage Stage, op string, track TrackRef, offset uint64, msg string) *Error {
	return &Error{Stage: stage, Op: op, Track: track, Offset: offset, Message: msg}
}

// WrapError attaches phase, track and offset context to an underlying error.
func WrapError(stage Stage, op string, track TrackRef, offset uint64, msg string, cause error) *Error {
	return &Error{Stage: stage, Op: op, Track: track, Offset: offset, Message: msg, Cause: cause}
}

// Errorf builds a formatted stage/track/offset error.
func Errorf(stage Stage, op string, track TrackRef, offset uint64, format string, args ...any) *Error {
	return NewError(stage, op, track, offset, fmt.Sprintf(format, args...))
}

// AsError extracts a *model.Error, preserving offsets across wrapping.
func AsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}
