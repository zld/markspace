// Package model defines the error model shared by every processing layer.
// Every defect carries the raw byte offset it was observed at, the track and
// representation involved, the processing layer that detected it and the
// normalized time interval it spans, so rollback decisions can be audited
// back to the original stream position.
package model

import (
	"fmt"

	"packcheck/internal/clock"
)

// Layer identifies the processing stage that produced an issue.
type Layer string

// Declared processing layers.
const (
	LayerClock     Layer = "clock"
	LayerSubtitle  Layer = "subtitle"
	LayerContainer Layer = "container"
	LayerSegment   Layer = "segment"
	LayerStream    Layer = "stream"
	LayerRecovery  Layer = "recovery"
	LayerReport    Layer = "report"
)

// Code is a stable machine-readable defect code.
type Code string

// Declared defect codes; unknown encodings are never auto-repaired.
const (
	CodeIllegalDropFrame Code = "illegal-dropframe"
	CodeWraparound       Code = "wraparound"
	CodeIncompatibleRate Code = "incompatible-rate"
	CodeInvertedCue      Code = "inverted-cue"
	CodeOverlappingCue   Code = "overlapping-cue"
	CodeEmptyCue         Code = "empty-cue"
	CodeCueAtCutPoint    Code = "cue-at-cutpoint"
	CodeMissingTrack     Code = "missing-track"
	CodeTimebaseMismatch Code = "timebase-mismatch"
	CodeBoundaryDrift    Code = "boundary-drift"
	CodeSlowConsumer     Code = "slow-consumer"
	CodeTruncatedLayer   Code = "truncated-layer"
	CodeUnsupportedInput Code = "unsupported-input"
	CodePathTraversal    Code = "path-traversal"
)

// Severity ranks how a defect affects rollback eligibility.
type Severity string

// Severity levels. Error excludes a representation; Warning is reported only.
const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// Issue is one auditable defect.
type Issue struct {
	Code         Code                       `json:"code"`
	Severity     Severity                   `json:"severity"`
	Layer        Layer                      `json:"layer"`
	Track        string                     `json:"track,omitempty"`
	Representation string                   `json:"representation,omitempty"`
	ByteOffset   int64                      `json:"byte_offset"`
	Interval     *clock.NormalizedInterval  `json:"interval,omitempty"`
	Message      string                     `json:"message"`
}

// String renders a stable single-line form for shell comparison.
func (i Issue) String() string {
	s := fmt.Sprintf("%s/%s layer=%s offset=%d", i.Severity, i.Code, i.Layer, i.ByteOffset)
	if i.Track != "" {
		s += " track=" + i.Track
	}
	if i.Representation != "" {
		s += " representation=" + i.Representation
	}
	if i.Interval != nil {
		s += fmt.Sprintf(" interval=[%s,%s) frames=%d",
			i.Interval.Start, i.Interval.End, i.Interval.Frames)
	}
	return s + " " + i.Message
}

// IssueLog collects issues in observation order.
type IssueLog struct {
	issues []Issue
}

// Add appends an issue and returns it.
func (l *IssueLog) Add(i Issue) Issue {
	l.issues = append(l.issues, i)
	return i
}

// All returns the captured issues.
func (l *IssueLog) All() []Issue {
	out := make([]Issue, len(l.issues))
	copy(out, l.issues)
	return out
}

// HasError reports whether any error-severity issue was recorded.
func (l *IssueLog) HasError() bool {
	for _, i := range l.issues {
		if i.Severity == SeverityError {
			return true
		}
	}
	return false
}

// CountByCode returns per-code occurrence counts.
func (l *IssueLog) CountByCode() map[Code]int {
	m := make(map[Code]int)
	for _, i := range l.issues {
		m[i.Code]++
	}
	return m
}

// Filter returns a new log containing only issues for one representation.
func (l *IssueLog) Filter(representation string) []Issue {
	var out []Issue
	for _, i := range l.issues {
		if i.Representation == representation {
			out = append(out, i)
		}
	}
	return out
}
