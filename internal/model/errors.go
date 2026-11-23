package model

import (
	"fmt"
	"sort"
)

// ErrorKind enumerates every defect the pipeline can detect. Keeping the
// set closed makes policy decisions in repair explicit instead of stringly
// typed.
type ErrorKind string

const (
	KindTruncatedBox          ErrorKind = "truncated_box"
	KindBadBoxLength          ErrorKind = "bad_box_length"
	KindTimestampBackward     ErrorKind = "timestamp_backward"
	KindTimestampDuplicate    ErrorKind = "timestamp_duplicate"
	KindTimestampGap          ErrorKind = "timestamp_gap"
	KindSampleOutOfBounds     ErrorKind = "sample_out_of_bounds"
	KindSparseKeyframe        ErrorKind = "sparse_keyframe"
	KindMissingKeyframeIndex  ErrorKind = "missing_keyframe_index"
	KindChannelLayoutConflict ErrorKind = "channel_layout_conflict"
	KindSampleRateConflict    ErrorKind = "sample_rate_conflict"
	KindColorConflict         ErrorKind = "color_metadata_conflict"
	KindCyclicDependency      ErrorKind = "cyclic_dependency"
	KindLimitExceeded         ErrorKind = "limit_exceeded"
	KindPathTraversal         ErrorKind = "path_traversal"
	KindIntegerOverflow       ErrorKind = "integer_overflow"
)

// Severity distinguishes hard failures from policy-level warnings.
type Severity string

const (
	SeverityError   Severity = "error"
	SeverityWarning Severity = "warning"
)

// MediaError binds a defect to a track, an optional sample index and the
// timeline ranges it affects. TrackID 0 means a container-level defect.
type MediaError struct {
	Kind        ErrorKind `json:"kind"`
	Severity    Severity  `json:"severity"`
	TrackID     uint32    `json:"track_id"`
	SampleIndex int       `json:"sample_index"`
	Ranges      []Range   `json:"ranges,omitempty"`
	Detail      string    `json:"detail"`
}

func (e MediaError) String() string {
	base := fmt.Sprintf("%s(%s) track=%d", e.Kind, e.Severity, e.TrackID)
	if e.SampleIndex >= 0 {
		base += fmt.Sprintf(" sample=%d", e.SampleIndex)
	}
	if len(e.Ranges) > 0 {
		base += " ranges=" + DescribeRanges(e.Ranges)
	}
	if e.Detail != "" {
		base += ": " + e.Detail
	}
	return base
}

// ErrorSet is the shared error model passed from probe through repair to
// remux. It is safe for concurrent appends and always yields a deterministic
// ordering, which keeps output hashes reproducible.
type ErrorSet struct {
	errs []MediaError
}

// Add records a defect. Nil receiver is tolerated so callers can opt out.
func (s *ErrorSet) Add(e MediaError) {
	if s == nil {
		return
	}
	if e.Severity == "" {
		e.Severity = SeverityError
	}
	s.errs = append(s.errs, e)
}

// All returns errors in a canonical order: container first, then track id,
// sample index, kind.
func (s *ErrorSet) All() []MediaError {
	if s == nil {
		return nil
	}
	out := make([]MediaError, len(s.errs))
	copy(out, s.errs)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.TrackID != b.TrackID {
			return a.TrackID < b.TrackID
		}
		if a.SampleIndex != b.SampleIndex {
			return a.SampleIndex < b.SampleIndex
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Detail < b.Detail
	})
	return out
}

// ByTrack returns the subset belonging to one track (id 0 = container).
func (s *ErrorSet) ByTrack(trackID uint32) []MediaError {
	var out []MediaError
	for _, e := range s.All() {
		if e.TrackID == trackID {
			out = append(out, e)
		}
	}
	return out
}

// Has reports whether any error of the given kind exists.
func (s *ErrorSet) Has(kind ErrorKind) bool {
	if s == nil {
		return false
	}
	for _, e := range s.errs {
		if e.Kind == kind {
			return true
		}
	}
	return false
}

// Count returns the number of recorded defects.
func (s *ErrorSet) Count() int {
	if s == nil {
		return 0
	}
	return len(s.errs)
}

// RangesFor aggregates every range attached to errors of one kind on a
// track and merges adjacent/overlapping intervals.
func (s *ErrorSet) RangesFor(trackID uint32, kind ErrorKind) []Range {
	var rs []Range
	for _, e := range s.errs {
		if e.TrackID == trackID && e.Kind == kind {
			rs = append(rs, e.Ranges...)
		}
	}
	return JoinRanges(rs)
}
