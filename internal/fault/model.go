// Package fault defines stable error codes and byte offsets shared by every
// mediacore stage. A Fault is safe to serialize into a manifest report.
package fault

import (
	"errors"
	"fmt"
)

// Fault carries a stable code, the first offending byte offset (when known),
// the codec being processed, and a human readable detail. Exit is the process
// exit status that should accompany the fault (2 for rejected input, 3 for a
// resumable interruption).
type Fault struct {
	Code       Code   `json:"code"`
	Message    string `json:"message"`
	Codec      string `json:"codec,omitempty"`
	Offset     int64  `json:"offset,omitempty"`
	HasOffset  bool   `json:"has_offset,omitempty"`
	ExitStatus int    `json:"exit_status"`
}

func (f *Fault) Error() string {
	where := ""
	if f.Codec != "" {
		where = " codec=" + f.Codec
	}
	if f.HasOffset {
		where += fmt.Sprintf(" offset=%d", f.Offset)
	}
	return fmt.Sprintf("%s%s: %s", f.Code, where, f.Message)
}

// New builds a fault with an offending byte offset.
func New(code Code, offset int64, codec, format string, args ...any) *Fault {
	return &Fault{
		Code:       code,
		Message:    fmt.Sprintf(format, args...),
		Codec:      codec,
		Offset:     offset,
		HasOffset:  true,
		ExitStatus: exitFor(code),
	}
}

// Newf builds a fault without a byte offset.
func Newf(code Code, codec, format string, args ...any) *Fault {
	return &Fault{
		Code:       code,
		Message:    fmt.Sprintf(format, args...),
		Codec:      codec,
		ExitStatus: exitFor(code),
	}
}

func exitFor(code Code) int {
	switch code {
	case CodeMigrationInterrupted:
		return 3
	case CodeInternal, CodeFFmpegUnavailable, CodeFFmpegFailed:
		return 1
	default:
		return 2
	}
}

// ExitCode maps any error to the documented process status: 0 success, 2
// rejected input or quality, 3 resumable interruption, 1 internal failure.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var f *Fault
	if errors.As(err, &f) {
		if f.ExitStatus != 0 {
			return f.ExitStatus
		}
	}
	return 1
}

// As returns the fault contained in err, if any.
func As(err error) (*Fault, bool) {
	var f *Fault
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}
