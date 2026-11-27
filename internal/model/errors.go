package model

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Stage identifies the pipeline stage at which an error was detected.
type Stage string

const (
	StageManifest   Stage = "manifest"
	StageCache      Stage = "cache"
	StageIndex      Stage = "index"
	StagePlanner    Stage = "planner"
	StagePipeline   Stage = "pipeline"
	StageTransform  Stage = "transform"
	StageValidate   Stage = "validate"
	StageJournal    Stage = "journal"
	StageWorkspace  Stage = "workspace"
)

// StructuredError is the common error type across modules: every failure is
// anchored to a stage and, where a file position exists, to a 1-based frame
// number and a non-negative byte offset.
type StructuredError struct {
	Stage  Stage       `json:"stage"`
	Frame  FrameNumber `json:"frame"`
	Offset int64       `json:"offset"`
	Reason string      `json:"reason"`
	err    error
}

// Error implements error.
func (e *StructuredError) Error() string {
	var loc string
	if e.Frame > 0 {
		loc = fmt.Sprintf("%s/frame=%d/offset=%d", e.Stage, e.Frame, e.Offset)
	} else {
		loc = string(e.Stage)
	}
	if e.err != nil {
		return fmt.Sprintf("%s: %s: %v", loc, e.Reason, e.err)
	}
	return fmt.Sprintf("%s: %s", loc, e.Reason)
}

// Unwrap exposes the wrapped cause for errors.Is/As.
func (e *StructuredError) Unwrap() error { return e.err }

// NewError builds a structured error without an underlying cause.
func NewError(stage Stage, frame FrameNumber, offset int64, reason string) *StructuredError {
	if offset < 0 {
		offset = 0
	}
	return &StructuredError{Stage: stage, Frame: frame, Offset: offset, Reason: reason}
}

// WrapError anchors an existing error to a stage and position.
func WrapError(stage Stage, frame FrameNumber, offset int64, reason string, err error) *StructuredError {
	if offset < 0 {
		offset = 0
	}
	return &StructuredError{Stage: stage, Frame: frame, Offset: offset, Reason: reason, err: err}
}

// AsStructured extracts a StructuredError from err, if present.
func AsStructured(err error) (*StructuredError, bool) {
	var se *StructuredError
	if errors.As(err, &se) {
		return se, true
	}
	return nil, false
}

// ErrPathEscapesWorkspace is returned when a requested path resolves outside
// the workspace root.
var ErrPathEscapesWorkspace = errors.New("model: path escapes workspace root")

// SafeJoin joins root with the workspace-relative name and guarantees the
// result stays inside root. Absolute names and ".." traversal are rejected so
// no module can write or read outside the working directory.
func SafeJoin(root, name string) (string, error) {
	if filepath.IsAbs(name) {
		return "", fmt.Errorf("%w: absolute path %q", ErrPathEscapesWorkspace, name)
	}
	cleanRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", err
	}
	// Clean("/"+name) strips any leading traversal attempt; the explicit
	// ".." check below is defense in depth for relative inputs.
	if name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) ||
		strings.Contains(name, string(filepath.Separator)+".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrPathEscapesWorkspace, name)
	}
	joined := filepath.Join(cleanRoot, filepath.Clean("/"+name))
	rel, err := filepath.Rel(cleanRoot, joined)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrPathEscapesWorkspace, name)
	}
	return joined, nil
}

// AtomicWrite writes data to a temp file in dir and renames it onto target.
// The temp file uses a distinct prefix so recovery logic never treats it as a
// committed output; the rename is atomic on the same filesystem.
func AtomicWrite(target string, data []byte, perm os.FileMode) (err error) {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".uncommitted-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Chmod(tmpName, perm); err != nil {
		return err
	}
	return os.Rename(tmpName, target)
}
