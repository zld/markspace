// Package fault defines the single error object passed across the probe,
// demux, mapping, timeline, remux and recovery boundaries.
package fault

import (
	"errors"
	"fmt"
)

type Kind string

const (
	KindArgument      Kind = "argument"
	KindMapping       Kind = "mapping"
	KindPath          Kind = "path"
	KindProbe         Kind = "probe"
	KindBoxBounds     Kind = "box_out_of_bounds"
	KindTruncated     Kind = "sample_truncated"
	KindCorrupt       Kind = "sample_corrupt"
	KindRTP           Kind = "rtp"
	KindTimeline      Kind = "timeline"
	KindHash          Kind = "payload_hash"
	KindIndex         Kind = "index"
	KindCheckpoint    Kind = "checkpoint"
	KindUnrecoverable Kind = "unrecoverable_input"
	KindVerify        Kind = "verify"
)

// Error carries an input byte offset and, where known, the track/SSRC that
// owns the bad region. Recoverable means the region can be isolated and later
// independent regions may still be processed.
type Error struct {
	Kind        Kind   `json:"kind"`
	Path        string `json:"path,omitempty"`
	Offset      int64  `json:"offset"`
	TrackID     uint32 `json:"track_id,omitempty"`
	SSRC        uint32 `json:"ssrc,omitempty"`
	Position    string `json:"position,omitempty"`
	Recoverable bool   `json:"recoverable"`
	Err         error  `json:"-"`
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s", e.Kind)
	if e.Path != "" {
		s += " path=" + e.Path
	}
	if e.Position != "" {
		s += " at=" + e.Position
	}
	if e.Offset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.TrackID != 0 {
		s += fmt.Sprintf(" track=%d", e.TrackID)
	}
	if e.SSRC != 0 {
		s += fmt.Sprintf(" ssrc=%08x", e.SSRC)
	}
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

func New(kind Kind, format string, args ...any) *Error {
	return &Error{Kind: kind, Offset: -1, Err: fmt.Errorf(format, args...)}
}

func At(kind Kind, path string, offset int64, format string, args ...any) *Error {
	return &Error{Kind: kind, Path: path, Offset: offset, Err: fmt.Errorf(format, args...)}
}

func Track(kind Kind, path string, trackID uint32, offset int64, err error, recoverable bool) *Error {
	return &Error{Kind: kind, Path: path, TrackID: trackID, Offset: offset, Err: err, Recoverable: recoverable}
}

func Wrap(kind Kind, err error) *Error {
	var fe *Error
	if errors.As(err, &fe) {
		if fe.Kind == "" {
			fe.Kind = kind
		}
		return fe
	}
	return &Error{Kind: kind, Offset: -1, Err: err}
}

func As(err error) (*Error, bool) {
	var fe *Error
	return fe, errors.As(err, &fe)
}

// ExitCode maps terminal error classes to the contract's process codes.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var fe *Error
	if errors.As(err, &fe) {
		switch fe.Kind {
		case KindArgument, KindMapping, KindPath:
			return 2
		case KindUnrecoverable:
			return 3
		case KindVerify, KindHash:
			return 1
		}
	}
	if errors.Is(err, ErrUsage) {
		return 2
	}
	return 3
}

var ErrUsage = errors.New("usage error")
