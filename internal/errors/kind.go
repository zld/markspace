// Package errors defines the stable error categories shared by every
// archivecheck subsystem.
package errors

import (
	stderrors "errors"
	"fmt"
)

// Kind is a machine-readable failure class.
type Kind string

const (
	KindInvalidArgument Kind = "invalid_argument"
	KindManifest        Kind = "manifest_error"
	KindInputHash       Kind = "input_hash_mismatch"
	KindContainer       Kind = "container_error"
	KindTruncated       Kind = "truncated_sample_table"
	KindEditList        Kind = "invalid_edit_interval"
	KindNoKeyframe      Kind = "no_decodable_keyframe"
	KindPlan            Kind = "invalid_stage_transition"
	KindJournal         Kind = "journal_error"
	KindRecovery        Kind = "recovery_error"
	KindLayer           Kind = "layer_progress_conflict"
	KindBoundary        Kind = "boundary_mismatch"
	KindProvenance      Kind = "provenance_mismatch"
	KindIO              Kind = "io_error"
	KindInterrupted     Kind = "simulated_interruption"
)

// Error carries a category and the coordinates needed to identify an
// affected asset, interval, stage, or representation layer.
type Error struct {
	Kind          Kind   `json:"kind"`
	Stage         string `json:"stage,omitempty"`
	AssetID       string `json:"asset_id,omitempty"`
	IntervalIndex *int   `json:"interval_index,omitempty"`
	Bitrate       int64  `json:"bitrate,omitempty"`
	Path          string `json:"path,omitempty"`
	Err           error  `json:"-"`
}

func (e *Error) Error() string {
	msg := string(e.Kind)
	if e.Stage != "" {
		msg += " stage=" + e.Stage
	}
	if e.AssetID != "" {
		msg += " asset=" + e.AssetID
	}
	if e.IntervalIndex != nil {
		msg += fmt.Sprintf(" interval_index=%d", *e.IntervalIndex)
	}
	if e.Bitrate != 0 {
		msg += fmt.Sprintf(" bitrate=%d", e.Bitrate)
	}
	if e.Path != "" {
		msg += " path=" + e.Path
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *Error) Unwrap() error { return e.Err }

// E constructs a categorized error.
func E(kind Kind, msg string) *Error {
	return &Error{Kind: kind, Err: stderrors.New(msg)}
}

// Wrap attaches classification and coordinates to an existing error.
func Wrap(kind Kind, err error) *Error {
	if err == nil {
		return nil
	}
	return &Error{Kind: kind, Err: err}
}

// WithStage returns e with its pipeline stage set.
func (e *Error) WithStage(stage string) *Error { e.Stage = stage; return e }

// WithAsset returns e with an asset ID set.
func (e *Error) WithAsset(id string) *Error { e.AssetID = id; return e }

// WithInterval returns e with an edit-list interval index set.
func (e *Error) WithInterval(i int) *Error { e.IntervalIndex = &i; return e }

// WithBitrate returns e with a representation bitrate set.
func (e *Error) WithBitrate(b int64) *Error { e.Bitrate = b; return e }

// WithPath returns e with a filesystem path set.
func (e *Error) WithPath(path string) *Error { e.Path = path; return e }

// KindOf returns the category of the first *Error in err's chain.
func KindOf(err error) Kind {
	var ce *Error
	if stderrors.As(err, &ce) {
		return ce.Kind
	}
	return ""
}

// ExitCode maps an error to the terminal contract: 2 for bad arguments,
// 1 for every input/processing failure.
func ExitCode(err error) int {
	if KindOf(err) == KindInvalidArgument {
		return 2
	}
	if err != nil {
		return 1
	}
	return 0
}
