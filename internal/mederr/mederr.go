// Package mederr defines the unified error type shared by the clock,
// ingest, audio and batch modules. Every error carries the track it
// belongs to, the time range it affects (nanoseconds on the unified
// timeline) and a category so callers can route and report it.
package mederr

import "fmt"

// Category classifies errors for deterministic handling rules.
type Category string

const (
	CatParse    Category = "parse"    // malformed input bytes (bad packet, bad box)
	CatTimeline Category = "timeline" // non-monotonic, contradictory or wrapped time
	CatAudio    Category = "audio"    // unsupported rate/layout or render failure
	CatOverlap  Category = "overlap"  // late input overlapping committed output
	CatChecksum Category = "checksum" // chunk output failed verification
	CatIndex    Category = "index"    // checkpoint index corrupt or inconsistent
	CatIO       Category = "io"       // filesystem failures
	CatLimit    Category = "limit"    // input exceeds configured bounds
)

// Error is the single error shape used across the pipeline.
type Error struct {
	Category    Category `json:"category"`
	Track       string   `json:"track"`
	StartNanos  int64    `json:"start_nanos"`
	EndNanos    int64    `json:"end_nanos"`
	Recoverable bool     `json:"recoverable"`
	Message     string   `json:"message"`
}

func (e *Error) Error() string {
	loc := e.Track
	if e.StartNanos >= 0 {
		loc = fmt.Sprintf("%s[%dms,%dms]", e.Track, e.StartNanos/1e6, e.EndNanos/1e6)
	}
	return fmt.Sprintf("%s: %s: %s", e.Category, loc, e.Message)
}

// New builds an Error. Pass start=-1 when no time range applies.
func New(cat Category, track string, start, end int64, recoverable bool, format string, args ...any) *Error {
	return &Error{
		Category:    cat,
		Track:       track,
		StartNanos:  start,
		EndNanos:    end,
		Recoverable: recoverable,
		Message:     fmt.Sprintf(format, args...),
	}
}

// List collects errors; HasFatal reports whether any is unrecoverable.
type List struct {
	Errs []*Error `json:"errors"`
}

func (l *List) Add(errs ...*Error) { l.Errs = append(l.Errs, errs...) }

func (l *List) HasFatal() bool {
	for _, e := range l.Errs {
		if !e.Recoverable {
			return true
		}
	}
	return false
}

func (l *List) Len() int { return len(l.Errs) }
