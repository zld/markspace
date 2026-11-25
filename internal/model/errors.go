// Package model defines the error and status model shared by every
// pipeline stage (parse, timeline, events, planner, worker, verify).
package model

import (
	"errors"
	"fmt"
	"sort"
)

// Issue codes. These identifiers are stable: they appear in manifests and
// are cross-checked by the independent verifier.
const (
	CodeTruncatedBox     = "TRUNCATED_BOX"
	CodeMFHDGap          = "MFHD_GAP"
	CodeMFHDDup          = "MFHD_DUP"
	CodeTfdtRegression   = "TFDT_REGRESSION"
	CodeMissingMedia     = "MISSING_MEDIA"
	CodeEventMismatch    = "EVENT_MISMATCH"
	CodeUnsupportedAudio = "UNSUPPORTED_AUDIO"
	CodeColorMissing     = "COLOR_MISSING"
	CodeColorConverted   = "COLOR_CONVERTED"
	CodeBudgetExceeded   = "BUDGET_EXCEEDED"
)

// Severity classifies how an issue affects the pipeline.
type Severity int

const (
	SevInfo Severity = iota
	SevWarn
	SevFatal
)

func (s Severity) String() string {
	switch s {
	case SevInfo:
		return "info"
	case SevWarn:
		return "warn"
	case SevFatal:
		return "fatal"
	}
	return "unknown"
}

// Issue is a single structured finding produced by a pipeline stage.
type Issue struct {
	Code     string
	Severity Severity
	File     string
	Offset   int64 // byte offset in File, -1 when not applicable
	Message  string
}

func (i Issue) String() string {
	loc := i.File
	if i.Offset >= 0 {
		loc = fmt.Sprintf("%s@%d", i.File, i.Offset)
	}
	if loc == "" {
		loc = "-"
	}
	return fmt.Sprintf("%s [%s] %s (%s)", i.Severity, i.Code, i.Message, loc)
}

// Report accumulates issues from one pipeline run.
type Report struct {
	Issues []Issue
}

// Addf appends an issue. off must be -1 when no file offset applies.
func (r *Report) Addf(code string, sev Severity, file string, off int64, format string, args ...any) {
	r.Issues = append(r.Issues, Issue{
		Code:     code,
		Severity: sev,
		File:     file,
		Offset:   off,
		Message:  fmt.Sprintf(format, args...),
	})
}

// Count returns how many issues carry the given code.
func (r *Report) Count(code string) int {
	n := 0
	for _, i := range r.Issues {
		if i.Code == code {
			n++
		}
	}
	return n
}

// Counts returns issue totals keyed by code.
func (r *Report) Counts() map[string]int {
	m := map[string]int{}
	for _, i := range r.Issues {
		m[i.Code]++
	}
	return m
}

// CodeCount is a (code, total) pair for deterministic output.
type CodeCount struct {
	Code string
	N    int
}

// SortedCounts returns counts ordered by code for stable terminal output.
func (r *Report) SortedCounts() []CodeCount {
	m := r.Counts()
	out := make([]CodeCount, 0, len(m))
	for c, n := range m {
		out = append(out, CodeCount{Code: c, N: n})
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Code < out[b].Code })
	return out
}

// Fatals returns all fatal issues in order.
func (r *Report) Fatals() []Issue {
	var out []Issue
	for _, i := range r.Issues {
		if i.Severity == SevFatal {
			out = append(out, i)
		}
	}
	return out
}

// HasFatal reports whether any fatal issue was recorded.
func (r *Report) HasFatal() bool { return len(r.Fatals()) > 0 }

// Sentinel errors shared across pipeline stages. Callers match them with
// errors.Is to distinguish expected rejections from programming errors.
var (
	ErrUnsupportedLayout = errors.New("unsupported channel layout")
	ErrEventOutOfRange   = errors.New("event time outside media timeline")
	ErrBudgetExceeded    = errors.New("candidate exceeds budget")
	ErrTruncated         = errors.New("truncated box")
	ErrOverflow          = errors.New("timestamp overflow")
)

// AudioInfo describes an audio track's channel layout and sample rate.
type AudioInfo struct {
	Channels   int
	SampleRate uint32
}

// ColorInfo describes video color metadata. Empty fields mean unspecified.
type ColorInfo struct {
	Primaries string
	Transfer  string
	Matrix    string
}
