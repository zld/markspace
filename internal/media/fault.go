package media

import (
	"errors"
	"fmt"
)

// Phase identifies the subsystem that produced a Fault.
type Phase string

const (
	PhaseManifest Phase = "manifest"
	PhaseMP4      Phase = "mp4"
	PhaseRTP      Phase = "rtp"
	PhaseTimeline Phase = "timeline"
	PhaseRender   Phase = "render"
	PhaseJournal  Phase = "journal"
	PhaseMigrate  Phase = "migrate"
	PhaseFixture  Phase = "fixture"
	PhaseCLI      Phase = "cli"
)

// Code is a stable, machine-readable failure category.
type Code string

const (
	CodeInvalidArgument   Code = "INVALID_ARGUMENT"
	CodeSizeLimit         Code = "SIZE_LIMIT"
	CodePathEscape        Code = "PATH_ESCAPE"
	CodeDuplicateRef      Code = "DUPLICATE_REFERENCE"
	CodeIllegalDuration   Code = "ILLEGAL_DURATION"
	CodeInvalidManifest   Code = "INVALID_MANIFEST"
	CodeTruncatedMP4      Code = "TRUNCATED_MP4_SAMPLE"
	CodeInvalidMP4        Code = "INVALID_MP4"
	CodeTimeOverflow      Code = "TIME_OVERFLOW"
	CodeAmbiguousJump     Code = "AMBIGUOUS_TIME_JUMP"
	CodeUnrecoverableLoss Code = "RTP_UNRECOVERABLE_LOSS"
	CodeMissingTrack      Code = "MISSING_TRACK"
	CodeHashMismatch      Code = "HASH_MISMATCH"
	CodeAlreadyCommitted  Code = "ALREADY_COMMITTED"
	CodeInjectedCrash     Code = "INJECTED_CRASH"
	CodeMigrationFailed   Code = "MIGRATION_VALIDATION_FAILED"
	CodePublication       Code = "PUBLICATION_FAILED"
)

// Fault is the common error model shared by parsing, recovery, timeline,
// rendering, journaling and migration.
type Fault struct {
	Phase  Phase  `json:"phase"`
	Code   Code   `json:"code"`
	Source string `json:"source,omitempty"`
	Line   int    `json:"line,omitempty"`
	Offset int64  `json:"offset,omitempty"`
	Detail string `json:"detail,omitempty"`
	Err    error  `json:"-"`
}

func (f *Fault) Error() string {
	loc := f.Source
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", loc, f.Line)
	} else if f.Offset > 0 {
		loc = fmt.Sprintf("%s:offset=%d", loc, f.Offset)
	}
	msg := fmt.Sprintf("%s %s", f.Code, f.Phase)
	if loc != "" {
		msg += " at " + loc
	}
	if f.Detail != "" {
		msg += ": " + f.Detail
	}
	if f.Err != nil {
		msg += ": " + f.Err.Error()
	}
	return msg
}

func (f *Fault) Unwrap() error { return f.Err }

func NewFault(phase Phase, code Code, detail string) *Fault {
	return &Fault{Phase: phase, Code: code, Detail: detail}
}

func WrapFault(phase Phase, code Code, source string, err error) *Fault {
	var existing *Fault
	if errors.As(err, &existing) {
		cp := *existing
		if cp.Phase == "" {
			cp.Phase = phase
		}
		if cp.Source == "" {
			cp.Source = source
		}
		return &cp
	}
	return &Fault{Phase: phase, Code: code, Source: source, Err: err}
}

func AsFault(err error) (*Fault, bool) {
	var f *Fault
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}

func ExitCode(err error) int {
	f, ok := AsFault(err)
	if !ok {
		return 1
	}
	switch f.Code {
	case CodeInjectedCrash:
		return 75
	case CodePathEscape, CodeDuplicateRef, CodeIllegalDuration,
		CodeInvalidManifest, CodeTruncatedMP4, CodeInvalidMP4,
		CodeTimeOverflow, CodeAmbiguousJump, CodeSizeLimit,
		CodeInvalidArgument, CodeUnrecoverableLoss:
		return 2
	case CodeHashMismatch:
		return 65
	default:
		return 1
	}
}
