package timeline

import "fmt"

// Stage identifies a pipeline stage for error attribution.
type Stage string

const (
	StageScan      Stage = "scan"
	StageIndex     Stage = "index"
	StageReconcile Stage = "reconcile"
	StagePlan      Stage = "plan"
	StageAlign     Stage = "align"
	StageSegment   Stage = "segment"
	StageCommit    Stage = "commit"
	StageManifest  Stage = "manifest"
	StageVerify    Stage = "verify"
	StageRecover   Stage = "recover"
)

// Kind classifies errors across modules.
type Kind string

const (
	KindTruncated       Kind = "truncated_tail"
	KindCorruptUnit     Kind = "corrupt_unit"
	KindIndexMissing    Kind = "index_missing"
	KindIndexDuplicate  Kind = "index_duplicate"
	KindIndexOutOfRange Kind = "index_out_of_range"
	KindIndexMispoint   Kind = "index_mispoint"
	KindSkewExceeded    Kind = "skew_exceeded"
	KindQuotaExceeded   Kind = "quota_exceeded"
	KindIO              Kind = "io"
	KindInconsistent    Kind = "inconsistent"
)

// Error is the shared error model: it always carries the stage, the
// byte offset (-1 when not applicable) and the recovery action taken
// or advised, so that operators can act on log lines directly.
type Error struct {
	Stage  Stage
	Kind   Kind
	Offset int64
	Action string
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("stage=%s kind=%s offset=%d action=%s: %s",
		e.Stage, e.Kind, e.Offset, e.Action, e.Msg)
}

// New builds an *Error.
func New(stage Stage, kind Kind, offset int64, action, msg string) *Error {
	return &Error{Stage: stage, Kind: kind, Offset: offset, Action: action, Msg: msg}
}
