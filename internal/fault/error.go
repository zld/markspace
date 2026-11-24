// Package fault defines the single error record shared by every stage. A
// fault always carries the container offset where the problem was found and,
// whenever it can be determined, the track and the affected time range in
// nanoseconds.
package fault

import (
	"fmt"
	"sort"
	"strings"
)

// Code is a stable machine-readable fault identifier.
type Code string

const (
	TruncatedBox         Code = "truncated_box"
	InvalidLength        Code = "invalid_length"
	CorruptSegment       Code = "corrupt_segment"
	MissingIndex         Code = "missing_index"
	MissingRequiredTrack Code = "missing_required_track"
	DependencyCycle      Code = "dependency_cycle"
	CheckpointMismatch   Code = "checkpoint_mismatch"
	VerifyMismatch       Code = "verify_mismatch"
	UnrecoverableData    Code = "unrecoverable_data"
	InvalidArgument      Code = "invalid_argument"
	IOError              Code = "io_error"
)

// Error is one recorded anomaly. Recoverable reports whether the pipeline
// could still preserve the surrounding payloads.
type Error struct {
	Code        Code   `json:"error_code"`
	Offset      uint64 `json:"offset_bytes"`
	Track       string `json:"track,omitempty"`
	TrackID     uint32 `json:"track_id"`
	StartNS     int64  `json:"start_ns"`
	EndNS       int64  `json:"end_ns"`
	HasRange    bool   `json:"-"`
	Recoverable bool   `json:"recoverable"`
	Detail      string `json:"detail,omitempty"`
}

// New creates a fault without track attribution.
func New(code Code, offset uint64, format string, args ...any) *Error {
	return &Error{Code: code, Offset: offset, Recoverable: true, Detail: fmt.Sprintf(format, args...)}
}

// WithTrack attaches the owning track.
func (e *Error) WithTrack(key string, id uint32) *Error {
	e.Track, e.TrackID = key, id
	return e
}

// WithRange attaches the affected nanosecond interval.
func (e *Error) WithRange(startNS, endNS int64) *Error {
	e.StartNS, e.EndNS, e.HasRange = startNS, endNS, true
	return e
}

// WithRecoverable records whether the affected payload can still be used.
func (e *Error) WithRecoverable(v bool) *Error {
	e.Recoverable = v
	return e
}

// Error implements error.
func (e *Error) Error() string {
	return e.Line()
}

// Line renders the fault as stable key=value terminal output. Range fields are
// always emitted so a consumer can rely on their presence; when no range is
// known they are reported as -1.
func (e *Error) Line() string {
	var b strings.Builder
	fmt.Fprintf(&b, "error_code=%s offset_bytes=%d track_id=%d", e.Code, e.Offset, e.TrackID)
	if e.HasRange {
		fmt.Fprintf(&b, " start_ns=%d end_ns=%d", e.StartNS, e.EndNS)
	} else {
		b.WriteString(" start_ns=-1 end_ns=-1")
	}
	if e.Track != "" {
		fmt.Fprintf(&b, " track=%s", e.Track)
	}
	fmt.Fprintf(&b, " recoverable=%t", e.Recoverable)
	if e.Detail != "" {
		fmt.Fprintf(&b, " detail=%q", e.Detail)
	}
	return b.String()
}

// List is a set of faults with deterministic ordering helpers.
type List []*Error

// Add appends a non-nil fault.
func (l *List) Add(e *Error) {
	if e != nil {
		*l = append(*l, e)
	}
}

// Sort orders faults by offset, code and track.
func (l List) Sort() {
	sort.SliceStable(l, func(i, j int) bool {
		if l[i].Offset != l[j].Offset {
			return l[i].Offset < l[j].Offset
		}
		if l[i].Code != l[j].Code {
			return l[i].Code < l[j].Code
		}
		return l[i].Track < l[j].Track
	})
}

// Dedup removes exact duplicates after sorting.
func (l List) Dedup() List {
	l.Sort()
	out := l[:0]
	for i, e := range l {
		if i == 0 || *e != *out[len(out)-1] {
			out = append(out, e)
		}
	}
	return out
}

// ByCode counts faults per code.
func (l List) ByCode(code Code) int {
	n := 0
	for _, e := range l {
		if e.Code == code {
			n++
		}
	}
	return n
}

// HasTrack reports whether a fault was recorded for the given track key.
func (l List) HasTrack(key string) bool {
	for _, e := range l {
		if e.Track == key {
			return true
		}
	}
	return false
}

// Unrecoverable reports whether any recorded fault left payload unusable.
func (l List) Unrecoverable() bool {
	for _, e := range l {
		if !e.Recoverable {
			return true
		}
	}
	return false
}
