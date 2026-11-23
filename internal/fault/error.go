// Package fault defines the error model shared by ingest, timeline,
// transform, checkpoint and verify stages.
//
// Every recoverable defect carries the source file, the fragment sequence
// number (when known), the offending box type and the raw byte offset in the
// input file. Offsets are exact so callers can correlate a reported error with
// the byte where a fault was injected or observed.
package fault

import (
	"fmt"
	"sort"
	"strings"
)

// Code classifies a processing fault.
type Code string

const (
	CodeTruncatedHeader    Code = "truncated_header"
	CodeTruncatedBox       Code = "truncated_box"
	CodeOversizeBox        Code = "oversize_box"
	CodeInvalidBoxSize     Code = "invalid_box_size"
	CodeMissingField       Code = "missing_field"
	CodeBadPayload         Code = "bad_payload"
	CodeSampleOutOfRange   Code = "sample_out_of_range"
	CodeCorruptMdat        Code = "corrupt_mdat"
	CodeIndexGap           Code = "index_gap"
	CodeSequenceGap        Code = "sequence_gap"
	CodeDuplicateFragment  Code = "duplicate_fragment"
	CodeTfdtRegression     Code = "tfdt_regression"
	CodeUnmergeableTrack   Code = "unmergeable_track"
	CodeScanLimit          Code = "scan_limit"
	CodeDurationLimit      Code = "duration_limit"
	CodeOverflow           Code = "overflow"
	CodeNegativeDuration   Code = "negative_duration"
	CodeCheckpointMismatch Code = "checkpoint_mismatch"
	CodeCheckpointCorrupt  Code = "checkpoint_corrupt"
	CodeInvariant          Code = "invariant"
	CodeLimit              Code = "limit"
)

// Error is a structured, offset-bearing fault.
type Error struct {
	Code        Code   `json:"code"`
	File        string `json:"file,omitempty"`
	FragmentSeq uint32 `json:"fragment_seq,omitempty"`
	HasSeq      bool   `json:"has_seq,omitempty"`
	BoxType     string `json:"box_type,omitempty"`
	Offset      int64  `json:"offset"`
	Detail      string `json:"detail,omitempty"`
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(string(e.Code))
	if e.File != "" {
		fmt.Fprintf(&b, " file=%s", e.File)
	}
	if e.HasSeq {
		fmt.Fprintf(&b, " fragment=%d", e.FragmentSeq)
	}
	if e.BoxType != "" {
		fmt.Fprintf(&b, " box=%s", e.BoxType)
	}
	fmt.Fprintf(&b, " offset=%d", e.Offset)
	if e.Detail != "" {
		fmt.Fprintf(&b, ": %s", e.Detail)
	}
	return b.String()
}

// New builds a fault with all correlation fields populated.
func New(code Code, file string, offset int64, box string, detail string) *Error {
	return &Error{Code: code, File: file, Offset: offset, BoxType: box, Detail: detail}
}

// WithFragment records the mfhd sequence associated with the fault.
func (e *Error) WithFragment(seq uint32) *Error {
	e.FragmentSeq = seq
	e.HasSeq = true
	return e
}

// Collector accumulates faults in observation order.
type Collector struct {
	errs []*Error
}

func (c *Collector) Add(e *Error) *Error {
	if e != nil {
		c.errs = append(c.errs, e)
	}
	return e
}

// Addf appends a formatted fault and returns it.
func (c *Collector) Addf(code Code, file string, offset int64, box string, format string, args ...any) *Error {
	return c.Add(New(code, file, offset, box, fmt.Sprintf(format, args...)))
}

func (c *Collector) Errors() []*Error {
	out := make([]*Error, len(c.errs))
	copy(out, c.errs)
	return out
}

func (c *Collector) Len() int { return len(c.errs) }

func (c *Collector) Has(code Code) bool {
	_, ok := c.Count(code)
	return ok
}

func (c *Collector) Count(code Code) (int, bool) {
	n := 0
	for _, e := range c.errs {
		if e.Code == code {
			n++
		}
	}
	return n, n > 0
}

// Codes returns the distinct codes in stable sorted order.
func (c *Collector) Codes() []Code {
	set := make(map[Code]struct{}, len(c.errs))
	for _, e := range c.errs {
		set[e.Code] = struct{}{}
	}
	out := make([]Code, 0, len(set))
	for code := range set {
		out = append(out, code)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// FirstByOffset returns the fault with the smallest raw offset.
func (c *Collector) FirstByOffset() (*Error, bool) {
	if len(c.errs) == 0 {
		return nil, false
	}
	first := c.errs[0]
	for _, e := range c.errs[1:] {
		if e.Offset < first.Offset {
			first = e
		}
	}
	return first, true
}

// SortByOffset orders faults by file, then raw offset.
func SortByOffset(errs []*Error) {
	sort.SliceStable(errs, func(i, j int) bool {
		if errs[i].File != errs[j].File {
			return errs[i].File < errs[j].File
		}
		return errs[i].Offset < errs[j].Offset
	})
}
