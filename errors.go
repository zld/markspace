package mediacore

import (
	"errors"
	"fmt"
)

// Processing stages. Every error emitted by the kernel is tagged with the
// stage that produced it so reports can show where an item was rejected.
const (
	StagePlaylist = "playlist"
	StageSegment  = "segment"
	StageTimeline = "timeline"
	StageGraph    = "trackgraph"
	StageMetadata = "metadata"
	StageMapping  = "mapping"
	StageMerge    = "merge"
	StageCommit   = "commit"
	StageRecovery = "recovery"
)

// Sentinel conditions. Concrete *Error values wrap one of these so callers can
// classify failures with errors.Is regardless of the asset/track context.
var (
	ErrZeroTimescale   = errors.New("zero timescale")
	ErrOverflow        = errors.New("time conversion overflow")
	ErrRegression      = errors.New("timeline regression")
	ErrIllegalField    = errors.New("illegal or missing manifest field")
	ErrMissingSegment  = errors.New("missing segment")
	ErrDuplicate       = errors.New("duplicate coverage")
	ErrConflict        = errors.New("content conflict")
	ErrCycle           = errors.New("track dependency cycle")
	ErrUnknownLayout   = errors.New("unrecognized channel layout")
	ErrColorMissing    = errors.New("no usable color metadata")
	ErrBudgetExceeded  = errors.New("processing budget exceeded")
	ErrMemoryExceeded  = errors.New("memory budget exceeded")
	ErrAnomaly         = errors.New("rtp sequence anomaly")
	ErrIllegalDuration = errors.New("illegal segment duration")
)

// Error is the uniform failure model of the kernel. It carries the asset,
// track, processing stage and source byte offset alongside the wrapped
// condition, so every rejected item can be reported with its exact origin.
type Error struct {
	Asset  string
	Track  uint32
	Tracks []uint32 // full participating set (e.g. every track in a dependency cycle)
	Stage  string
	Offset int64 // byte offset inside the source manifest/segment; -1 if absent
	Op     error
	Msg    string
}

// NewError builds a contextual error. Offset -1 means "not source-located".
func NewError(asset string, track uint32, stage string, offset int64, msg string) *Error {
	return &Error{Asset: asset, Track: track, Stage: stage, Offset: offset, Msg: msg}
}

// Wrap attaches the full asset/track/stage/offset context to a condition.
func Wrap(err error, asset string, track uint32, stage string, offset int64) *Error {
	return &Error{Asset: asset, Track: track, Stage: stage, Offset: offset, Op: err}
}

// WrapMsg is Wrap with an additional human-readable detail.
func WrapMsg(err error, asset string, track uint32, stage string, offset int64, msg string) *Error {
	return &Error{Asset: asset, Track: track, Stage: stage, Offset: offset, Op: err, Msg: msg}
}

// Error renders a single deterministic, greppable line.
func (e *Error) Error() string {
	loc := e.Asset
	if loc == "" {
		loc = "-"
	}
	s := fmt.Sprintf("mediacore error: stage=%s asset=%s track=%d offset=%d",
		e.Stage, loc, e.Track, e.Offset)
	if e.Op != nil {
		s += " condition=" + e.Op.Error()
	}
	if e.Msg != "" {
		s += " detail=" + e.Msg
	}
	return s
}

// Is matches on the wrapped condition, allowing errors.Is(e, ErrCycle) etc.
func (e *Error) Is(target error) bool {
	if e.Op == nil {
		return false
	}
	return errors.Is(e.Op, target)
}

// Unwrap exposes the wrapped condition to errors.Is/errors.As.
func (e *Error) Unwrap() error { return e.Op }

// ReStage re-anchors an existing *Error at a new stage/source offset without
// nesting another *Error, so rendered messages stay flat and greppable.
func ReStage(err error, asset string, track uint32, stage string, offset int64) error {
	var me *Error
	if errors.As(err, &me) {
		cp := *me
		if asset != "" {
			cp.Asset = asset
		}
		if track != 0 {
			cp.Track = track
		}
		cp.Stage, cp.Offset = stage, offset
		return &cp
	}
	return Wrap(err, asset, track, stage, offset)
}
