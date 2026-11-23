package clock

import (
	"errors"
	"fmt"
)

// Category classifies pipeline errors so callers can map them to exit
// codes and recovery strategies.
type Category string

const (
	// CatParse marks structural input failures (bad boxes, truncation).
	CatParse Category = "parse"
	// CatRepair marks failures while normalizing sequences or timestamps.
	CatRepair Category = "repair"
	// CatIndex marks index construction and persistence failures.
	CatIndex Category = "index"
	// CatIO marks filesystem failures.
	CatIO Category = "io"
	// CatUnrecoverable marks fragments that cannot be repaired and must
	// never reach the index.
	CatUnrecoverable Category = "unrecoverable"
	// CatInvariant marks violated pipeline invariants.
	CatInvariant Category = "invariant"
)

// Error is the shared structured error of the pipeline. Fragment is the
// logical or input-order fragment number when known, Offset the byte
// offset inside the offending input when known; both are -1/0 otherwise.
type Error struct {
	Cat      Category
	Track    uint32
	Fragment int64
	Offset   int64
	Msg      string
	Err      error
}

func (e *Error) Error() string {
	s := fmt.Sprintf("%s: track=%d fragment=%d offset=%d: %s",
		e.Cat, e.Track, e.Fragment, e.Offset, e.Msg)
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap returns the wrapped cause, if any.
func (e *Error) Unwrap() error { return e.Err }

// HasCategory reports whether err or any wrapped cause is a pipeline error
// of the given category.
func HasCategory(err error, c Category) bool {
	var ce *Error
	if errors.As(err, &ce) {
		return ce.Cat == c
	}
	return false
}
