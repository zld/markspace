// Package model defines the shared vocabulary of the media routing pipeline:
// rational time conversion, error categories, fragments, tracks, index
// entries and the delivery manifest.
package model

import (
	"errors"
	"fmt"
	"math"
)

// Micros is an absolute instant on the unified microsecond timeline.
// It is derived from track ticks with exact integer rational arithmetic;
// floats are never used, so repeated conversions cannot accumulate drift.
type Micros int64

// MicrosPerSecond is the unified time base denominator.
const MicrosPerSecond int64 = 1_000_000

// Category classifies an error or an issue. Every category can travel
// across pipeline stages without losing its meaning.
type Category string

const (
	CategoryTruncatedBox      Category = "truncated_box"
	CategoryLengthOverflow    Category = "length_overflow"
	CategoryInvalidBox        Category = "invalid_box"
	CategoryUnrecoverable     Category = "unrecoverable_boundary"
	CategoryMissingInit       Category = "missing_init"
	CategoryZeroTimescale     Category = "zero_timescale"
	CategoryCorruptIndex      Category = "corrupt_index"
	CategoryIndexMismatch     Category = "index_mismatch"
	CategoryManifestMismatch  Category = "manifest_mismatch"
	CategoryPathTraversal     Category = "path_traversal"
	CategoryInvalidConfig     Category = "invalid_config"
	CategoryCanceled          Category = "canceled"
	CategoryDownstream        Category = "downstream_failure"
	CategoryRTPSync           Category = "rtp_sync"
	CategoryIO                Category = "io_error"
	CategoryRecoveredBoundary Category = "recovered_boundary"
	CategoryMFHDGap           Category = "mfhd_gap"
	CategoryDuplicateSequence Category = "duplicate_sequence"
	CategoryTfdtRegressed     Category = "tfdt_regressed"
)

// TaskError is a locatable failure. It records which stage failed, where in
// the input the failure occurred and what category it belongs to.
type TaskError struct {
	Category Category `json:"category"`
	Stage    string   `json:"stage"`
	Offset   uint64   `json:"offset,omitempty"`
	TrackID  uint32   `json:"track_id,omitempty"`
	Detail   string   `json:"detail,omitempty"`
	Err      error    `json:"-"`
}

// Error implements error.
func (e *TaskError) Error() string {
	loc := ""
	if e.Offset != 0 {
		loc = fmt.Sprintf(" offset=%d", e.Offset)
	}
	if e.TrackID != 0 {
		loc += fmt.Sprintf(" track=%d", e.TrackID)
	}
	base := fmt.Sprintf("%s: stage=%s%s", e.Category, e.Stage, loc)
	if e.Detail != "" {
		base += ": " + e.Detail
	}
	if e.Err != nil {
		return base + ": " + e.Err.Error()
	}
	return base
}

// Unwrap exposes the wrapped cause.
func (e *TaskError) Unwrap() error { return e.Err }

// NewTaskError builds a TaskError.
func NewTaskError(category Category, stage, detail string) *TaskError {
	return &TaskError{Category: category, Stage: stage, Detail: detail}
}

// WrapTaskError builds a TaskError around a cause.
func WrapTaskError(category Category, stage, detail string, err error) *TaskError {
	return &TaskError{Category: category, Stage: stage, Detail: detail, Err: err}
}

// WithOffset returns a copy located at the given byte offset.
func (e *TaskError) WithOffset(offset uint64) *TaskError {
	cp := *e
	cp.Offset = offset
	return &cp
}

// WithTrack returns a copy attributed to the given track.
func (e *TaskError) WithTrack(trackID uint32) *TaskError {
	cp := *e
	cp.TrackID = trackID
	return &cp
}

// AsTaskError extracts a TaskError from an error chain.
func AsTaskError(err error) (*TaskError, bool) {
	var te *TaskError
	if errors.As(err, &te) {
		return te, true
	}
	return nil, false
}

// Issue is a non-fatal, auditable anomaly. Issues are attached to fragments
// and repeated in the manifest so that every repair can be cross-checked.
type Issue struct {
	Category     Category `json:"category"`
	Stage        string   `json:"stage"`
	Offset       uint64   `json:"offset,omitempty"`
	DeclaredSize uint64   `json:"declared_size,omitempty"`
	Available    uint64   `json:"available,omitempty"`
	TrackID      uint32   `json:"track_id,omitempty"`
	Sequence     uint32   `json:"sequence,omitempty"`
	Detail       string   `json:"detail,omitempty"`
}

// ByteRange is a half-open byte interval [Start, End) in the source file.
type ByteRange struct {
	Start uint64 `json:"start"`
	End   uint64 `json:"end"`
}

// Len returns the number of bytes covered by the range.
func (r ByteRange) Len() uint64 {
	if r.End <= r.Start {
		return 0
	}
	return r.End - r.Start
}

// Valid reports whether the range is non-negative.
func (r ByteRange) Valid() bool { return r.End >= r.Start }

// Empty reports whether the range covers no bytes.
func (r ByteRange) Empty() bool { return r.End <= r.Start }

// RangeOf builds a half-open range from an offset and a size, saturating on
// overflow instead of wrapping.
func RangeOf(offset, size uint64) ByteRange {
	if offset > math.MaxUint64-size {
		return ByteRange{Start: offset, End: math.MaxUint64}
	}
	return ByteRange{Start: offset, End: offset + size}
}

// TicksToMicros converts track ticks to the unified microsecond base with
// exact rational arithmetic, rounding halves up. The multiplication is
// overflow-checked; no floating point accumulation takes place.
func TicksToMicros(ticks uint64, timescale uint32) (Micros, error) {
	if timescale == 0 {
		return 0, NewTaskError(CategoryZeroTimescale, "time", "track timescale is zero")
	}
	scale := uint64(timescale)
	if ticks > math.MaxUint64/uint64(MicrosPerSecond) {
		return 0, NewTaskError(CategoryLengthOverflow, "time",
			fmt.Sprintf("ticks=%d overflows microsecond conversion", ticks))
	}
	product := ticks * uint64(MicrosPerSecond)
	quotient := product / scale
	remainder := product % scale
	if remainder*2 >= scale {
		quotient++
	}
	if quotient > math.MaxInt64 {
		return 0, NewTaskError(CategoryLengthOverflow, "time",
			fmt.Sprintf("microsecond value %d exceeds int64", quotient))
	}
	return Micros(quotient), nil
}

// DurationMicros converts a tick duration to microseconds using the same
// exact arithmetic as TicksToMicros.
func DurationMicros(ticks uint64, timescale uint32) (Micros, error) {
	return TicksToMicros(ticks, timescale)
}

// MustTicksToMicros is a convenience for validated fixture data.
func MustTicksToMicros(ticks uint64, timescale uint32) Micros {
	m, err := TicksToMicros(ticks, timescale)
	if err != nil {
		panic(err)
	}
	return m
}

// AbsMicros returns the absolute difference of two instants.
func AbsMicros(a, b Micros) Micros {
	if a > b {
		return a - b
	}
	return b - a
}
