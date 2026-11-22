package model

import "fmt"

// Stage identifies the pipeline phase a Fault originates from.
type Stage string

const (
	StageParse   Stage = "parse"
	StageAlign   Stage = "align"
	StageCompose Stage = "compose"
	StageRender  Stage = "render"
	StageVerify  Stage = "verify"
)

// Fault is a pipeline error that always carries the stage, and when
// applicable the input file offset, the track number and the unified
// timestamp at which processing failed. Parse faults must preserve the
// raw byte offset so reviewers can inspect the damaged structure.
type Fault struct {
	Stage  Stage     `json:"stage"`
	Track  int       `json:"track"`          // -1 when not track-specific
	Offset int64     `json:"error_offset"`   // byte offset into the input, -1 when n/a
	Time   *Rational `json:"time,omitempty"` // unified time context, when known
	Msg    string    `json:"msg"`
}

func (f *Fault) Error() string {
	s := fmt.Sprintf("stage=%s", f.Stage)
	if f.Track >= 0 {
		s += fmt.Sprintf(" track=%d", f.Track)
	}
	if f.Offset >= 0 {
		s += fmt.Sprintf(" error_offset=%d", f.Offset)
	}
	if f.Time != nil {
		s += fmt.Sprintf(" time=%s", f.Time)
	}
	return s + ": " + f.Msg
}

// ParseFault builds a parse-stage fault pinned to a raw byte offset.
func ParseFault(offset int64, track int, format string, args ...any) *Fault {
	return &Fault{Stage: StageParse, Track: track, Offset: offset, Msg: fmt.Sprintf(format, args...)}
}

// StageFault builds a fault for a non-parse stage. offset may be -1.
func StageFault(stage Stage, track int, offset int64, t *Rational, format string, args ...any) *Fault {
	return &Fault{Stage: stage, Track: track, Offset: offset, Time: t, Msg: fmt.Sprintf(format, args...)}
}

// AsFault extracts a *Fault from err, if present.
func AsFault(err error) (*Fault, bool) {
	if err == nil {
		return nil, false
	}
	if f, ok := err.(*Fault); ok {
		return f, true
	}
	return nil, false
}
