// Package media provides the shared time, error and track model used by
// demux adapters and the delivery pipeline.
package media

import (
	"errors"
	"fmt"
)

// Phase identifies the pipeline stage an error originated from.
type Phase string

const (
	PhaseProbe    Phase = "probe"
	PhaseDemux    Phase = "demux"
	PhaseMap      Phase = "map"
	PhaseTimeline Phase = "timeline"
	PhaseIndex    Phase = "index"
	PhaseRemux    Phase = "remux"
	PhaseVerify   Phase = "verify"
)

// TrackType classifies an elementary stream.
type TrackType string

const (
	TrackVideo TrackType = "video"
	TrackAudio TrackType = "audio"
	TrackData  TrackType = "data"
)

// Error is the staged error model. Every failure carries the phase, the
// track (-1 when not track specific), a byte offset (-1 when unknown) and
// both the raw and normalized timestamps involved (-1 when not applicable),
// so post-mortems can locate the failure without re-running the demux.
type Error struct {
	Phase          Phase
	Track          int
	Offset         int64
	RawTime        int64
	NormalizedTime int64
	Unrecoverable  bool
	Msg            string
	Err            error
}

func (e *Error) Error() string {
	s := fmt.Sprintf("phase=%s track=%d offset=%d raw=%d norm=%d: %s",
		e.Phase, e.Track, e.Offset, e.RawTime, e.NormalizedTime, e.Msg)
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// New builds a staged error wrapping err.
func New(phase Phase, track int, offset int64, msg string, err error) *Error {
	return &Error{Phase: phase, Track: track, Offset: offset,
		RawTime: -1, NormalizedTime: -1, Msg: msg, Err: err}
}

// WithTimes attaches raw and normalized timestamps to the error.
func (e *Error) WithTimes(raw, norm int64) *Error {
	e.RawTime, e.NormalizedTime = raw, norm
	return e
}

// Fatal marks the error unrecoverable: the delivery must abort with a
// non-zero exit code instead of emitting a partial manifest.
func (e *Error) Fatal() *Error {
	e.Unrecoverable = true
	return e
}

// IsUnrecoverable reports whether err (or anything it wraps) is a staged
// error flagged unrecoverable.
func IsUnrecoverable(err error) bool {
	var me *Error
	if errors.As(err, &me) {
		return me.Unrecoverable
	}
	return false
}

// AsPhase unwraps err into target and reports whether the staged error
// belongs to the given phase.
func AsPhase(err error, target **Error, phase Phase) bool {
	if errors.As(err, target) {
		return (*target).Phase == phase
	}
	return false
}

// Gap records a recoverable hole in a track: samples [FromSeq, ToSeq) are
// missing or damaged. Gaps are surfaced in the delivery manifest rather
// than silently dropped.
type Gap struct {
	TrackID int
	FromSeq int
	ToSeq   int
	Reason  string
}
