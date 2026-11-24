// Package model defines the shared media clock, track identity and the
// classifiable diagnostics used by every subsystem.
package model

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"
)

// Rat is an exact, immutable rational number. Times in this project are never
// stored as floating point so that repeated runs produce identical hashes.
type Rat struct {
	r big.Rat
}

// NewRat builds an exact rational. A zero denominator is rejected.
func NewRat(num, den int64) (Rat, error) {
	if den == 0 {
		return Rat{}, errors.New("rational denominator is zero")
	}
	var r big.Rat
	r.SetFrac64(num, den)
	return Rat{r: r}, nil
}

// MustRat is NewRat for constants and tests.
func MustRat(num, den int64) Rat {
	r, err := NewRat(num, den)
	if err != nil {
		panic(err)
	}
	return r
}

// RatFromInt converts an integer to a rational.
func RatFromInt(n int64) Rat { return MustRat(n, 1) }

// ParseRat accepts either "num/den" or a decimal string.
func ParseRat(s string) (Rat, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rat{}, errors.New("empty rational")
	}
	var r big.Rat
	if _, ok := r.SetString(s); !ok {
		return Rat{}, fmt.Errorf("invalid rational %q", s)
	}
	return Rat{r: r}, nil
}

// RatFromDuration converts a time.Duration to seconds.
func RatFromDuration(d time.Duration) Rat {
	return MustRat(int64(d), int64(time.Second))
}

// String returns the canonical reduced form ("3", "1001/30000").
func (r Rat) String() string { return r.r.RatString() }

// Seconds is only for human-facing reports, never for equality or hashing.
func (r Rat) Seconds() float64 {
	f, _ := r.r.Float64()
	return f
}

func (r Rat) Add(o Rat) Rat {
	var out big.Rat
	out.Add(&r.r, &o.r)
	return Rat{r: out}
}

func (r Rat) Sub(o Rat) Rat {
	var out big.Rat
	out.Sub(&r.r, &o.r)
	return Rat{r: out}
}

func (r Rat) Mul(o Rat) Rat {
	var out big.Rat
	out.Mul(&r.r, &o.r)
	return Rat{r: out}
}

func (r Rat) Quo(o Rat) (Rat, error) {
	if o.r.Sign() == 0 {
		return Rat{}, errors.New("division by zero rational")
	}
	var out big.Rat
	out.Quo(&r.r, &o.r)
	return Rat{r: out}, nil
}

// Cmp returns -1, 0 or 1.
func (r Rat) Cmp(o Rat) int { return r.r.Cmp(&o.r) }

// Equal reports exact equality.
func (r Rat) Equal(o Rat) bool { return r.r.Cmp(&o.r) == 0 }

// Sign returns -1, 0 or 1.
func (r Rat) Sign() int { return r.r.Sign() }

// IsZero reports whether the value is exactly zero.
func (r Rat) IsZero() bool { return r.r.Sign() == 0 }

// MarshalText emits the canonical reduced form for deterministic encoding.
func (r Rat) MarshalText() ([]byte, error) { return []byte(r.r.RatString()), nil }

// UnmarshalText is the inverse of MarshalText.
func (r *Rat) UnmarshalText(b []byte) error {
	v, err := ParseRat(string(b))
	if err != nil {
		return err
	}
	*r = v
	return nil
}

// Kind classifies a track in the unified timeline.
type Kind string

const (
	KindVideo    Kind = "video"
	KindAudio    Kind = "audio"
	KindSubtitle Kind = "subtitle"
	KindTimecode Kind = "timecode"
	KindRTP      Kind = "rtp"
)

// Track identifies a source without depending on map iteration order.
type Track struct {
	ID   string
	Kind Kind
}

func (t Track) String() string { return string(t.Kind) + ":" + t.ID }

// Span is a half-open interval [Start, End) on the unified media clock.
type Span struct {
	Start Rat
	End   Rat
}

// Duration returns End-Start; callers must only use it for non-reversed spans.
func (s Span) Duration() Rat { return s.End.Sub(s.Start) }

// Contains reports Start <= t < End.
func (s Span) Contains(t Rat) bool { return s.Start.Cmp(t) <= 0 && t.Cmp(s.End) < 0 }

// InputPos pinpoints bad input. Line and Column are 1-based; Offset is 0-based.
type InputPos struct {
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Column int    `json:"column,omitempty"`
	Offset int64  `json:"offset,omitempty"`
}

func (p InputPos) String() string {
	var b strings.Builder
	if p.File != "" {
		b.WriteString(p.File)
	}
	if p.Line > 0 {
		fmt.Fprintf(&b, ":%d", p.Line)
		if p.Column > 0 {
			fmt.Fprintf(&b, ":%d", p.Column)
		}
	}
	if p.Offset != 0 || b.Len() == 0 {
		fmt.Fprintf(&b, "@%d", p.Offset)
	}
	return b.String()
}

// Error codes are stable strings so they can be asserted from the terminal.
type ErrorCode string

const (
	CodeInvalidFrameNumber ErrorCode = "invalid_frame_number"
	CodeInvalidTimecode    ErrorCode = "invalid_timecode"
	CodeInvalidMode        ErrorCode = "invalid_timecode_mode"
	CodeDriftExceeded      ErrorCode = "clock_drift_exceeded"
	CodeSubtitleMalformed  ErrorCode = "subtitle_malformed"
	CodeSubtitleZero       ErrorCode = "subtitle_zero_duration"
	CodeSubtitleReversed   ErrorCode = "subtitle_reversed"
	CodeSubtitleOverlap    ErrorCode = "subtitle_overlap"
	CodeIndexFallback      ErrorCode = "index_fallback"
	CodeIndexCorrupt       ErrorCode = "index_corrupt"
	CodeTruncatedSample    ErrorCode = "truncated_sample"
	CodeSampleRead         ErrorCode = "sample_read"
	CodeRenderNoSample     ErrorCode = "no_sample_at_time"
	CodeRenderDecode       ErrorCode = "decode_failed"
	CodeTimelineAlign      ErrorCode = "timeline_align"
	CodeCheckpointMismatch ErrorCode = "checkpoint_mismatch"
	CodeCheckpointCorrupt  ErrorCode = "checkpoint_corrupt"
	CodeInvalidArgument    ErrorCode = "invalid_argument"
	CodeIO                 ErrorCode = "io_error"
	CodeInternal           ErrorCode = "internal_error"
)

// Fatal reports whether a code aborts the run (exit code 1). Everything else
// is a per-segment problem that leaves other tracks running (exit code 2).
func (c ErrorCode) Fatal() bool {
	switch c {
	case CodeCheckpointMismatch, CodeCheckpointCorrupt,
		CodeInvalidArgument, CodeIO, CodeInternal, CodeIndexCorrupt:
		return true
	default:
		return false
	}
}

// Error is the single diagnostic type shared by all subsystems.
type Error struct {
	Code      ErrorCode `json:"code"`
	Subsystem string    `json:"subsystem"`
	Track     string    `json:"track,omitempty"`
	Pos       InputPos  `json:"pos"`
	Message   string    `json:"message"`
	Warning   bool      `json:"warning,omitempty"`
}

func (e *Error) Error() string {
	loc := e.Pos.String()
	if loc == "" {
		loc = "-"
	}
	return fmt.Sprintf("%s: %s at %s track=%s: %s",
		e.Subsystem, e.Code, loc, dash(e.Track), e.Message)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Recoverable means the failure is local to one segment/track.
func (e *Error) Recoverable() bool { return !e.Warning && !e.Code.Fatal() }

// NewError constructs a non-warning diagnostic.
func NewError(code ErrorCode, subsystem, track string, pos InputPos, format string, args ...any) *Error {
	return &Error{Code: code, Subsystem: subsystem, Track: track, Pos: pos,
		Message: fmt.Sprintf(format, args...)}
}

// NewWarning constructs an informational diagnostic.
func NewWarning(code ErrorCode, subsystem, track string, pos InputPos, format string, args ...any) *Error {
	e := NewError(code, subsystem, track, pos, format, args...)
	e.Warning = true
	return e
}

// Errors is an insertion-ordered diagnostic collection.
type Errors []*Error

func (es *Errors) Add(e *Error) {
	if e != nil {
		*es = append(*es, e)
	}
}

func (es Errors) HasRecoverable() bool {
	for _, e := range es {
		if e.Recoverable() {
			return true
		}
	}
	return false
}

func (es Errors) RecoverableCount() int {
	n := 0
	for _, e := range es {
		if e.Recoverable() {
			n++
		}
	}
	return n
}

func (es Errors) WarningCount() int {
	n := 0
	for _, e := range es {
		if e.Warning {
			n++
		}
	}
	return n
}

// FirstFatal returns the first non-recoverable diagnostic, if any.
func (es Errors) FirstFatal() *Error {
	for _, e := range es {
		if e.Code.Fatal() {
			return e
		}
	}
	return nil
}
