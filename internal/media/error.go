package media

import (
	"errors"
	"fmt"
	"strings"
)

// FailureKind maps a structured failure to the command's stable exit codes.
type FailureKind string

const (
	KindCorrupt     FailureKind = "corrupt"
	KindSpec        FailureKind = "spec_mismatch"
	KindUnsupported FailureKind = "unsupported"
)

// Error carries enough location information for batch processing logs.
// Offset is zero-based; -1 means that the location does not have a byte offset.
type Error struct {
	Kind        FailureKind `json:"kind"`
	Code        string      `json:"code"`
	Message     string      `json:"message"`
	ElementPath string      `json:"element_path,omitempty"`
	Offset      int64       `json:"offset"`
	TrackID     int         `json:"track_id,omitempty"`
	BlockIndex  int         `json:"block_index,omitempty"`
	Detail      string      `json:"detail,omitempty"`
	Err         error       `json:"-"`
}

func newError(kind FailureKind, code, message string) *Error {
	return &Error{Kind: kind, Code: code, Message: message, Offset: -1}
}

func Corrupt(code, message string) *Error {
	return newError(KindCorrupt, code, message)
}

func SpecMismatch(code, message string) *Error {
	return newError(KindSpec, code, message)
}

func Unsupported(code, message string) *Error {
	return newError(KindUnsupported, code, message)
}

func (e *Error) WithElement(path string, offset int64) *Error {
	cp := *e
	cp.ElementPath = path
	cp.Offset = offset
	return &cp
}

func (e *Error) WithTrack(trackID, blockIndex int) *Error {
	cp := *e
	cp.TrackID = trackID
	cp.BlockIndex = blockIndex
	return &cp
}

func (e *Error) WithDetail(detail string) *Error {
	cp := *e
	cp.Detail = detail
	return &cp
}

func (e *Error) Wrap(err error) *Error {
	cp := *e
	cp.Err = err
	return &cp
}

func (e *Error) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s", e.Code, e.Message)
	if e.ElementPath != "" {
		fmt.Fprintf(&b, " path=%s", e.ElementPath)
	}
	if e.Offset >= 0 {
		fmt.Fprintf(&b, " offset=%d", e.Offset)
	}
	if e.TrackID != 0 {
		fmt.Fprintf(&b, " track=%d", e.TrackID)
	}
	if e.BlockIndex > 0 {
		fmt.Fprintf(&b, " block=%d", e.BlockIndex)
	}
	if e.Detail != "" {
		fmt.Fprintf(&b, " detail=%q", e.Detail)
	}
	if e.Err != nil {
		fmt.Fprintf(&b, ": %v", e.Err)
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// AsError finds the structured error in err's wrap chain.
func AsError(err error) (*Error, bool) {
	var target *Error
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// ExitCode implements the command contract:
// 0 pass, 2 corrupt/unsupported input, 3 specification mismatch.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	if e, ok := AsError(err); ok {
		switch e.Kind {
		case KindSpec:
			return 3
		case KindCorrupt, KindUnsupported:
			return 2
		}
	}
	return 1
}
