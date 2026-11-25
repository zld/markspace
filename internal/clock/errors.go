// Package clock defines the common event timeline and track-local media clocks.
package clock

import (
	"errors"
	"fmt"
)

// Category is a stable, machine-readable failure class used by the CLI and tests.
type Category string

const (
	CategoryInvalidArgument     Category = "invalid_argument"
	CategoryInvalidBox          Category = "invalid_box"
	CategoryTruncatedBox        Category = "truncated_box"
	CategoryTruncatedRTP        Category = "truncated_rtp_packet"
	CategoryInvalidRTP          Category = "invalid_rtp_packet"
	CategoryInvalidEditList     Category = "invalid_edit_list"
	CategoryNegativeDuration    Category = "negative_duration_edit"
	CategoryTimestampWrap       Category = "timestamp_wrap_ambiguous"
	CategoryOverlappingInterval Category = "overlapping_interval"
	CategoryMissingKeyframe     Category = "missing_keyframe_index"
	CategoryMissingTrack        Category = "missing_track"
	CategoryOutOfRange          Category = "out_of_range"
	CategoryTimebase            Category = "timebase_incompatible"
	CategoryHashMismatch        Category = "hash_mismatch"
	CategoryState               Category = "state_error"
	CategoryIO                  Category = "io_error"
)

// Error is the unified error model shared by parsing, indexing and pipeline code.
//
// Offset is always a byte offset in Path. For manifest or logical errors without
// a more precise byte position it is zero, which is itself a valid raw offset.
type Error struct {
	Category Category `json:"category"`
	Path     string   `json:"path"`
	Track    int      `json:"track_number"`
	Offset   int64    `json:"raw_offset"`
	Reason   string   `json:"reason"`
	Err      error    `json:"-"`
}

func (e *Error) Error() string {
	loc := fmt.Sprintf("%s track=%d offset=%d", e.Path, e.Track, e.Offset)
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %s: %v", e.Category, loc, e.Reason, e.Err)
	}
	return fmt.Sprintf("%s: %s: %s", e.Category, loc, e.Reason)
}

func (e *Error) Unwrap() error { return e.Err }

// New constructs a located media-core error.
func New(category Category, path string, track int, offset int64, reason string) *Error {
	return &Error{Category: category, Path: path, Track: track, Offset: offset, Reason: reason}
}

// Wrap adds location and classification to an underlying error.
func Wrap(category Category, path string, track int, offset int64, reason string, err error) *Error {
	return &Error{Category: category, Path: path, Track: track, Offset: offset, Reason: reason, Err: err}
}

// As finds the first *Error in err's tree.
func As(err error) (*Error, bool) {
	var located *Error
	if errors.As(err, &located) {
		return located, true
	}
	return nil, false
}

// Classification returns category and a non-negative raw offset for reporting.
// Errors outside this package are converted to an invalid-argument location so
// the CLI can still satisfy the stable diagnostic contract.
func Classification(err error, fallbackPath string) (Category, int64, int) {
	var located *Error
	if errors.As(err, &located) {
		off := located.Offset
		if off < 0 {
			off = 0
		}
		return located.Category, off, located.Track
	}
	return CategoryInvalidArgument, 0, 0
}
