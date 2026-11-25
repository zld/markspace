package clock

import "fmt"

// Category names the failure class reported by recovery. The set is closed so
// checkpoint manifests and CLI output stay stable across runs.
type Category string

const (
	// CategoryTruncated: a box header announced more bytes than file remaining.
	CategoryTruncated Category = "truncated_box"
	// CategoryBounds: a declared length or offset fell outside the file.
	CategoryBounds Category = "out_of_bounds"
	// CategoryMissingIndex: an index box (stco/stsc/stsz/stts) was absent.
	CategoryMissingIndex Category = "missing_index"
	// CategoryCorruptSample: a sample's bytes failed integrity checks.
	CategoryCorruptSample Category = "corrupt_sample"
	// CategoryNoSync: no verifiable sync sample exists at or after a point.
	CategoryNoSync Category = "no_sync_point"
	// CategoryEmpty: the container held no usable media.
	CategoryEmpty Category = "empty_media"
)

// OffsetError attaches a real input byte position to any failure. Offset is
// always the original file position, never a post-recovery substitute.
type OffsetError struct {
	Offset   Offset
	Category Category
	Detail   string
	Wrapped  error
}

func (e *OffsetError) Error() string {
	if e.Wrapped != nil {
		return fmt.Sprintf("%s at byte %d: %s: %v", e.Category, e.Offset, e.Detail, e.Wrapped)
	}
	return fmt.Sprintf("%s at byte %d: %s", e.Category, e.Offset, e.Detail)
}

func (e *OffsetError) Unwrap() error { return e.Wrapped }

// NewOffsetError builds a categorised failure pinned to a byte offset.
func NewOffsetError(off Offset, cat Category, detail string, wrapped error) *OffsetError {
	return &OffsetError{Offset: off, Category: cat, Detail: detail, Wrapped: wrapped}
}

// OffsetOf extracts the byte position from any error, if it carries one.
func OffsetOf(err error) (Offset, bool) {
	var oe *OffsetError
	if asOffsetError(err, &oe) {
		return oe.Offset, true
	}
	return 0, false
}

func asOffsetError(err error, target **OffsetError) bool {
	for err != nil {
		if e, ok := err.(*OffsetError); ok {
			*target = e
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// ErrUnrecoverable says a media timeline has no verifiable restart point. Its
// presence forces the CLI to exit non-zero rather than emit a partial plan.
type ErrUnrecoverable struct {
	Reason string
	Offset Offset
}

func (e *ErrUnrecoverable) Error() string {
	return fmt.Sprintf("media unrecoverable at byte %d: %s", e.Offset, e.Reason)
}

// ErrStageOrder is returned when state transitions are attempted out of order.
type ErrStageOrder struct {
	From, To string
}

func (e *ErrStageOrder) Error() string {
	return fmt.Sprintf("illegal stage transition %s -> %s", e.From, e.To)
}

// ErrDigestMismatch prevents reusing a checkpoint produced by a different
// input. Reusing one would silently mask non-determinism.
type ErrDigestMismatch struct {
	Expected, Actual string
}

func (e *ErrDigestMismatch) Error() string {
	return fmt.Sprintf("checkpoint digest mismatch: expected %s, got %s", e.Expected, e.Actual)
}
