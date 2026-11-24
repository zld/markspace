// Package fault defines the structured error model shared by the scanner,
// repairer, indexer and pipeline. Every fault carries enough context to point
// at the exact byte, track and processing stage that produced it.
package fault

import (
	"errors"
	"fmt"
)

// Kind classifies a fault so callers can decide whether it is a corrupt input
// (exit code 2) or an operational failure (exit code 1).
type Kind string

const (
	// KindCorrupt marks structurally invalid container data.
	KindCorrupt Kind = "corrupt"
	// KindTimestamp marks timestamps that cannot be repaired unambiguously.
	KindTimestamp Kind = "timestamp"
	// KindInvariant marks an internal consistency check that failed.
	KindInvariant Kind = "invariant"
	// KindIO marks a filesystem or stream failure.
	KindIO Kind = "io"
	// KindCancel marks work abandoned because a peer failed.
	KindCancel Kind = "cancel"
)

// Stage names the pipeline stage that observed the fault.
type Stage string

const (
	StageScan     Stage = "scan"
	StageRead     Stage = "read"
	StageClock    Stage = "clock"
	StageRepair   Stage = "repair"
	StageIndex    Stage = "index"
	StageWrite    Stage = "write"
	StageVerify   Stage = "verify"
	StagePipeline Stage = "pipeline"
)

// Fault is the canonical error value used across all packages.
type Fault struct {
	Kind      Kind   `json:"kind"`
	Stage     Stage  `json:"stage"`
	File      string `json:"file,omitempty"`
	Offset    int64  `json:"offset"` // -1 when not byte-addressable
	TrackID   uint32 `json:"track_id"`
	Timescale uint32 `json:"timescale"`
	Box       string `json:"box,omitempty"`
	Message   string `json:"message"`
	Cause     error  `json:"-"`
}

// New builds a Fault with an unknown offset.
func New(kind Kind, stage Stage, format string, args ...any) *Fault {
	return &Fault{Kind: kind, Stage: stage, Offset: -1, Message: fmt.Sprintf(format, args...)}
}

// At attaches an absolute byte offset to f and returns it.
func (f *Fault) At(offset int64) *Fault {
	f.Offset = offset
	return f
}

// OnTrack attaches track and timescale context to f and returns it.
func (f *Fault) OnTrack(trackID, timescale uint32) *Fault {
	f.TrackID = trackID
	f.Timescale = timescale
	return f
}

// In attaches the input file name and returns f.
func (f *Fault) In(file string) *Fault {
	f.File = file
	return f
}

// WithBox records the box type being parsed and returns f.
func (f *Fault) WithBox(box string) *Fault {
	f.Box = box
	return f
}

// Wrap stores the underlying error without changing the message.
func (f *Fault) Wrap(cause error) *Fault {
	f.Cause = cause
	return f
}

// Error implements error.
func (f *Fault) Error() string {
	where := "offset=unknown"
	if f.Offset >= 0 {
		where = fmt.Sprintf("offset=%d", f.Offset)
	}
	loc := ""
	if f.File != "" {
		loc = " file=" + f.File
	}
	box := ""
	if f.Box != "" {
		box = " box=" + f.Box
	}
	track := ""
	if f.TrackID != 0 || f.Timescale != 0 {
		track = fmt.Sprintf(" track=%d timescale=%d", f.TrackID, f.Timescale)
	}
	msg := f.Message
	if f.Cause != nil {
		msg += ": " + f.Cause.Error()
	}
	return fmt.Sprintf("%s/%s%s%s%s %s %s", f.Kind, f.Stage, loc, box, track, where, msg)
}

// Unwrap exposes the wrapped cause.
func (f *Fault) Unwrap() error { return f.Cause }

// AsFault extracts a *Fault from err, if present.
func AsFault(err error) (*Fault, bool) {
	var f *Fault
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}

// ExitCode maps errors to the CLI contract: 0 success, 2 corrupt input,
// 1 any other failure.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	f, ok := AsFault(err)
	if !ok {
		return 1
	}
	switch f.Kind {
	case KindCorrupt, KindTimestamp:
		return 2
	default:
		return 1
	}
}

// Corrupt is a convenience constructor for structural faults.
func Corrupt(stage Stage, file string, offset int64, box, format string, args ...any) *Fault {
	return New(KindCorrupt, stage, format, args...).In(file).At(offset).WithBox(box)
}

// Timestamp is a convenience constructor for unresolvable timestamp faults.
func Timestamp(stage Stage, file string, offset int64, trackID, timescale uint32, format string, args ...any) *Fault {
	return New(KindTimestamp, stage, format, args...).In(file).At(offset).OnTrack(trackID, timescale)
}
