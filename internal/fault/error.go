// Package fault defines the single error model shared by all subsystems.
// Every failure carries the pipeline stage, the track or representation it
// belongs to, the input path and the raw byte offset where processing
// became impossible, so failures are locatable and reruns verifiable.
package fault

import (
	"errors"
	"fmt"
)

// Stage identifies the pipeline stage that produced an error.
type Stage string

const (
	StageInput        Stage = "input"            // input/boundary validation
	StageManifest     Stage = "manifest"         // MPD / M3U8 parsing
	StageTimeline     Stage = "timeline"         // unified timeline construction
	StageBoxHeader    Stage = "parse-box-header" // truncated box header
	StageSampleTable  Stage = "parse-sample-table"
	StageMediaPayload Stage = "parse-media-payload"
	StageRepair       Stage = "repair" // timestamp repair
	StageIndex        Stage = "index"  // random-access index rebuild
	StageJournal      Stage = "journal"
	StageCommit       Stage = "commit" // atomic output commit
	StageVerify       Stage = "verify" // metric threshold validation
)

// Error is the structured failure type used across the pipeline.
type Error struct {
	Stage  Stage  // pipeline stage
	Track  string // track / representation id, "" if not applicable
	Path   string // input path, "" if not applicable
	Offset int64  // raw byte offset in Path, -1 if unknown
	Entry  int    // manifest entry index, -1 if not applicable
	Msg    string
	Err    error // wrapped cause, may be nil
}

func (e *Error) Error() string {
	s := fmt.Sprintf("stage=%s", e.Stage)
	if e.Track != "" {
		s += fmt.Sprintf(" track=%s", e.Track)
	}
	if e.Path != "" {
		s += fmt.Sprintf(" path=%s", e.Path)
	}
	if e.Offset >= 0 {
		s += fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.Entry >= 0 {
		s += fmt.Sprintf(" entry=%d", e.Entry)
	}
	s += ": " + e.Msg
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

func (e *Error) Unwrap() error { return e.Err }

// New builds an Error with Offset and Entry defaulting to "not applicable".
func New(stage Stage, msg string) *Error {
	return &Error{Stage: stage, Offset: -1, Entry: -1, Msg: msg}
}

// Wrap attaches a cause to an Error.
func (e *Error) Wrap(err error) *Error {
	c := *e
	c.Err = err
	return &c
}

// At records the input path and raw byte offset.
func (e *Error) At(path string, offset int64) *Error {
	c := *e
	c.Path = path
	c.Offset = offset
	return &c
}

// For records the track/representation and manifest entry.
func (e *Error) For(track string, entry int) *Error {
	c := *e
	c.Track = track
	c.Entry = entry
	return &c
}

// Exit codes required by the CLI contract.
const (
	ExitOK          = 0 // processing and threshold checks passed
	ExitValidation  = 2 // input or boundary validation failed
	ExitCommitPhase = 3 // resume or output commit failed
)

// ExitCode maps an error to the process exit code. Errors from the
// journal/commit stages map to 3; every other structured fault maps to 2;
// unknown errors map to 2 as well (they are validation failures by default).
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var fe *Error
	if errors.As(err, &fe) {
		switch fe.Stage {
		case StageJournal, StageCommit:
			return ExitCommitPhase
		}
	}
	return ExitValidation
}
