// Package errs defines the shared pipeline error model. Every failure
// carries a stage, an asset, a stable machine-readable code and a source
// offset so callers can locate the fault and map it to an exit code.
package errs

import "fmt"

// Stage identifies the pipeline subsystem that raised an error.
type Stage string

const (
	StageArgs     Stage = "args"
	StageIngest   Stage = "ingest"
	StageTimeline Stage = "timeline"
	StageIndex    Stage = "index"
	StageRelease  Stage = "release"
)

// Stable error codes. These are part of the CLI contract and must not
// change without a version bump of the acceptance harness.
const (
	CodeTruncatedSample = "TRUNCATED_SAMPLE"
	CodeBadContainer    = "BAD_CONTAINER"
	CodeBadPacketStream = "BAD_PACKET_STREAM"
	CodeAmbiguousOrder  = "AMBIGUOUS_ORDER"
	CodeTimeConflict    = "TIME_CONFLICT"
	CodeChecksum        = "CHECKSUM_MISMATCH"
	CodeInterrupted     = "INTERRUPTED"
	CodeNoRollback      = "NO_ROLLBACK_TARGET"
	CodeInternal        = "INTERNAL"
)

// Error is the unified error model shared by ingest, index and release.
type Error struct {
	Stage  Stage  // subsystem that failed
	Code   string // stable machine-readable code
	Asset  string // affected asset/track (may be empty)
	Offset int64  // byte offset or sample index of the fault (-1 if n/a)
	Msg    string // human-readable detail
}

func (e *Error) Error() string {
	return fmt.Sprintf("stage=%s code=%s asset=%q offset=%d: %s",
		e.Stage, e.Code, e.Asset, e.Offset, e.Msg)
}

// New builds an *Error with a formatted message.
func New(stage Stage, code, asset string, offset int64, format string, args ...any) *Error {
	return &Error{Stage: stage, Code: code, Asset: asset, Offset: offset,
		Msg: fmt.Sprintf(format, args...)}
}

// Wrap converts a plain error into an *Error at the given stage/offset.
func Wrap(stage Stage, code, asset string, offset int64, err error) *Error {
	if e, ok := err.(*Error); ok {
		return e
	}
	return &Error{Stage: stage, Code: code, Asset: asset, Offset: offset, Msg: err.Error()}
}
