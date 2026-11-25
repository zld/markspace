// Package fault defines stable error classifications and located media intervals.
package fault

import (
	"errors"
	"fmt"
	"strings"
)

// Class is a machine-readable failure category.
type Class string

const (
	ClassManifest       Class = "manifest"
	ClassPath           Class = "path"
	ClassBoxLength      Class = "box_length_overflow"
	ClassTruncated      Class = "truncated_box"
	ClassEditList       Class = "edit_list_out_of_bounds"
	ClassNonZeroStart   Class = "non_zero_start"
	ClassSparseKeyframe Class = "sparse_keyframes"
	ClassMissingTrack   Class = "missing_track"
	ClassTimeBase       Class = "invalid_timebase"
	ClassDuplicate      Class = "duplicate_id"
	ClassBudget         Class = "budget_infeasible"
	ClassQuality        Class = "quality_below_minimum"
	ClassChecksum       Class = "checksum_mismatch"
	ClassCheckpoint     Class = "checkpoint_conflict"
	ClassChunk          Class = "chunk_failed"
	ClassIO             Class = "io_error"
	ClassUnsupported    Class = "unsupported_structure"
)

// Interval locates an anomaly in track, sample, byte and unified time coordinates.
type Interval struct {
	Kind        string `json:"kind"`
	Class       Class  `json:"class"`
	TrackID     uint32 `json:"track_id,omitempty"`
	TrackNumber int    `json:"track_number,omitempty"`
	StartSample int64  `json:"start_sample,omitempty"`
	EndSample   int64  `json:"end_sample,omitempty"`
	ByteStart   int64  `json:"byte_start,omitempty"`
	ByteEnd     int64  `json:"byte_end,omitempty"`
	StartMS     int64  `json:"start_ms"`
	EndMS       int64  `json:"end_ms"`
	Message     string `json:"message"`
	Reject      bool   `json:"reject"`
	Quarantine  bool   `json:"quarantine"`
}

// Error is an item-scoped processing failure carrying all discovered intervals.
type Error struct {
	Class     Class      `json:"class"`
	ItemID    string     `json:"item_id,omitempty"`
	Path      string     `json:"path,omitempty"`
	Message   string     `json:"message"`
	Intervals []Interval `json:"intervals,omitempty"`
	Err       error      `json:"-"`
}

func (e *Error) Error() string {
	var b strings.Builder
	if e.ItemID != "" {
		fmt.Fprintf(&b, "item %s: ", e.ItemID)
	}
	fmt.Fprintf(&b, "%s: %s", e.Class, e.Message)
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// New constructs a classified error.
func New(class Class, message string, args ...any) *Error {
	return &Error{Class: class, Message: fmt.Sprintf(message, args...)}
}

// Wrap classifies an existing error.
func Wrap(class Class, err error, message string, args ...any) *Error {
	return &Error{Class: class, Message: fmt.Sprintf(message, args...), Err: err}
}

// AsError performs errors.As for *Error.
func AsError(err error) (*Error, bool) {
	var fe *Error
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

// Rejection reports whether the class makes an item undeliverable.
func Rejection(class Class) bool {
	switch class {
	case ClassBoxLength, ClassTruncated, ClassEditList, ClassMissingTrack,
		ClassTimeBase, ClassDuplicate, ClassBudget, ClassQuality,
		ClassChecksum, ClassCheckpoint, ClassUnsupported:
		return true
	default:
		return false
	}
}
