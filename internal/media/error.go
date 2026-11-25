package media

import "errors"

// Category classifies every error produced inside the project so that the
// command adapter can print a stable, reviewable error category and pick a
// process exit code.
type Category string

const (
	// CatInvalidInput means the input data itself is malformed or
	// truncated (for example a cut WAV header or a partial frame).
	CatInvalidInput Category = "invalid_input"
	// CatLayout means channel layout / frame layout validation failed.
	CatLayout Category = "layout_mismatch"
	// CatCheckpoint means a checkpoint record could not be verified and
	// had to be rolled back to the last valid boundary.
	CatCheckpoint Category = "checkpoint_corrupt"
	// CatUnrecoverable means processing cannot continue at all.
	CatUnrecoverable Category = "unrecoverable"
	// CatIO means a local filesystem operation failed.
	CatIO Category = "io_error"
	// CatState means stored index state is inconsistent with the inputs.
	CatState Category = "state_conflict"
)

// Error is a categorized error. Errors returned by subsystems can be
// inspected with errors.As to recover the category.
type Error struct {
	Cat Category
	Msg string
	Err error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return string(e.Cat) + ": " + e.Msg + ": " + e.Err.Error()
	}
	return string(e.Cat) + ": " + e.Msg
}

func (e *Error) Unwrap() error { return e.Err }

// NewError builds a categorized error without a wrapped cause.
func NewError(cat Category, msg string) *Error {
	return &Error{Cat: cat, Msg: msg}
}

// WrapError builds a categorized error wrapping cause.
func WrapError(cat Category, msg string, cause error) *Error {
	return &Error{Cat: cat, Msg: msg, Err: cause}
}

// CategoryOf returns the category of err, or CatUnrecoverable when the
// error carries no known category.
func CategoryOf(err error) Category {
	var me *Error
	if errors.As(err, &me) {
		return me.Cat
	}
	return CatUnrecoverable
}
