// Package mediaerr is the shared structured error model. Every pipeline stage
// attaches a category, a master-clock timestamp (when one is known), the
// affected track and whether the condition can be recovered downstream.
package mediaerr

import (
	"errors"
	"fmt"
)

// Category classifies where and why a failure happened.
type Category string

const (
	CatValidation    Category = "validation"    // malformed input / limit breach
	CatManifest      Category = "manifest"      // DASH/HLS structural problem
	CatDiscontinuity Category = "discontinuity" // HLS sequence break, isolated segment
	CatTimebase      Category = "timebase"      // rational conversion / overflow
	CatTransport     Category = "transport"     // RTP loss, duplicates, late packets
	CatAlignment     Category = "alignment"     // cross-representation misalignment
	CatRemux         Category = "remux"         // MP4 writing / verification
	CatCanceled      Category = "canceled"      // context cancellation
	CatInternal      Category = "internal"      // invariant violations / bugs
)

// Error is the structured error carried across the pipeline.
type Error struct {
	Category    Category `json:"category"`
	Code        string   `json:"code"`
	Message     string   `json:"message"`
	TrackID     string   `json:"track_id,omitempty"`
	AtTick      uint64   `json:"at_tick,omitempty"` // master 90 kHz ticks
	Recoverable bool     `json:"recoverable"`
	cause       error
}

func (e *Error) Error() string {
	loc := ""
	if e.TrackID != "" {
		loc = " track=" + e.TrackID
	}
	if e.AtTick != 0 {
		loc += fmt.Sprintf(" tick=%d", e.AtTick)
	}
	rec := ""
	if e.Recoverable {
		rec = " [recoverable]"
	}
	return fmt.Sprintf("%s/%s:%s%s%s", e.Category, e.Code, e.Message, loc, rec)
}

// Unwrap supports errors.Is/As on wrapped causes.
func (e *Error) Unwrap() error { return e.cause }

// New builds a structured error.
func New(cat Category, code, msg string) *Error {
	return &Error{Category: cat, Code: code, Message: msg}
}

// With annotates an existing media error with location metadata.
func (e *Error) With(track string, tick uint64, recoverable bool) *Error {
	cp := *e
	cp.TrackID, cp.AtTick, cp.Recoverable = track, tick, recoverable
	return &cp
}

// Wrap attaches a generic cause for errors.Is chains.
func (e *Error) Wrap(cause error) *Error {
	cp := *e
	cp.cause = cause
	return &cp
}

// Sentinel base errors.
var (
	ErrCanceled  = errors.New("pipeline canceled")
	ErrLimit     = errors.New("configured limit exceeded")
	ErrMalformed = errors.New("malformed media input")
)

// As extracts a *Error from any wrapped error chain.
func As(err error) (*Error, bool) {
	var me *Error
	if errors.As(err, &me) {
		return me, true
	}
	return nil, false
}

// Report is the serializable error ledger emitted in the output manifest.
type Report struct {
	Items []*Error `json:"items"`
}

// Add appends an item; nil reports are tolerated.
func (r *Report) Add(e *Error) {
	if e != nil {
		r.Items = append(r.Items, e)
	}
}

// HasCategory reports whether any recorded item matches.
func (r *Report) HasCategory(c Category) bool {
	for _, e := range r.Items {
		if e.Category == c {
			return true
		}
	}
	return false
}

// RecoverableCount returns how many items are marked recoverable.
func (r *Report) RecoverableCount() int {
	n := 0
	for _, e := range r.Items {
		if e.Recoverable {
			n++
		}
	}
	return n
}

// UnrecoveredCount returns non-recoverable items.
func (r *Report) UnrecoveredCount() int {
	n := 0
	for _, e := range r.Items {
		if !e.Recoverable {
			n++
		}
	}
	return n
}
