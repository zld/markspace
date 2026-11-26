// Package apperr defines the single structured error used across stages.
//
// The location fields are all optional except Code and Message.  A zero
// Sample or ByteOffset means that the error is not tied to a concrete sample.
package apperr

import (
	"errors"
	"fmt"
	"strings"
)

const (
	ExitOK          = 0
	ExitGeneric     = 1
	ExitInvalid     = 2
	ExitIsolated    = 3
	ExitInterrupted = 4
)

// E is the unified error structure shared by manifest, clock, scan and jobs.
type E struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	Stage       string `json:"stage,omitempty"`
	Track       string `json:"track,omitempty"`
	SourceFile  string `json:"source_file,omitempty"`
	Field       string `json:"field,omitempty"`
	Sample      int    `json:"sample,omitempty"`
	ByteOffset  int64  `json:"byte_offset,omitempty"`
	Recoverable bool   `json:"recoverable"`
}

// New creates an error with a stable machine-readable code.
func New(code, format string, args ...any) *E {
	return &E{Code: code, Message: fmt.Sprintf(format, args...)}
}

// WithStage fills the stage when it is not already set.
func (e *E) WithStage(stage string) *E {
	if e.Stage == "" {
		e.Stage = stage
	}
	return e
}

// WithTrack fills track and source when absent.
func (e *E) WithTrack(track, source string) *E {
	if e.Track == "" {
		e.Track = track
	}
	if e.SourceFile == "" {
		e.SourceFile = source
	}
	return e
}

// WithSample attaches a one-based sample number and source byte offset.
func (e *E) WithSample(sample int, offset int64) *E {
	e.Sample = sample
	e.ByteOffset = offset
	return e
}

// WithField attaches a manifest JSON field path.
func (e *E) WithField(field string) *E {
	e.Field = field
	return e
}

// WithRecoverable marks whether another invocation may continue safely.
func (e *E) WithRecoverable(recoverable bool) *E {
	e.Recoverable = recoverable
	return e
}

func (e *E) Error() string {
	var b strings.Builder
	b.WriteString(e.Code)
	b.WriteString(": ")
	b.WriteString(e.Message)
	if e.Stage != "" {
		fmt.Fprintf(&b, " stage=%s", e.Stage)
	}
	if e.Track != "" {
		fmt.Fprintf(&b, " track=%s", e.Track)
	}
	if e.Field != "" {
		fmt.Fprintf(&b, " field=%s", e.Field)
	}
	if e.SourceFile != "" {
		fmt.Fprintf(&b, " source=%s", e.SourceFile)
	}
	if e.Sample > 0 {
		fmt.Fprintf(&b, " sample=%d", e.Sample)
	}
	if e.ByteOffset > 0 {
		fmt.Fprintf(&b, " byte_offset=%d", e.ByteOffset)
	}
	return b.String()
}

// As finds the nearest *E in err's wrap chain.
func As(err error) (*E, bool) {
	var target *E
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}

// ExitCode maps structured codes to the terminal adapter's process codes.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	pe, ok := As(err)
	if !ok {
		return ExitGeneric
	}
	switch {
	case pe.Code == "E_TRACK_ISOLATED":
		return ExitIsolated
	case pe.Code == "E_FAULT_INJECTED" || pe.Code == "E_BUDGET_PAUSED":
		return ExitInterrupted
	case strings.HasPrefix(pe.Code, "E_MANIFEST"),
		strings.HasPrefix(pe.Code, "E_PATH"),
		strings.HasPrefix(pe.Code, "E_TRACK_"),
		pe.Code == "E_CYCLE",
		pe.Code == "E_SAMPLE_RANGE",
		pe.Code == "E_MP4":
		return ExitInvalid
	default:
		return ExitGeneric
	}
}
