// Package fault records processing failures with the stage, the input
// segment and the original byte offset inside that segment where the
// problem was observed. Records deliberately avoid absolute host paths so
// that reports can be exchanged without leaking local environment details.
package fault

import "fmt"

// Stage identifies the pipeline phase that produced a record.
type Stage string

const (
	StageManifest  Stage = "manifest"
	StageParse     Stage = "parse"
	StageTransform Stage = "transform"
	StageValidate  Stage = "validate"
)

// Severity ranks records. Errors stop safe processing of the affected
// region; warnings (for example an explicit discontinuity marker) do not.
type Severity string

const (
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Kind enumerates the condition classes the kernel recognises.
type Kind string

const (
	KindTruncatedBox      Kind = "truncated_box"
	KindOversizedBox      Kind = "oversized_box"
	KindMissingSegment    Kind = "missing_segment"
	KindDuplicateSequence Kind = "duplicate_sequence"
	KindDiscontinuity     Kind = "discontinuity"
	KindTimestampJump     Kind = "negative_timestamp_jump"
	KindWrapExpanded      Kind = "wrap_expanded"
	KindRoundtripMismatch Kind = "roundtrip_mismatch"
	KindHashMismatch      Kind = "hash_mismatch"
)

// Record is a single, reproducible finding.
type Record struct {
	Stage    Stage    `json:"stage"`
	Severity Severity `json:"severity"`
	Kind     Kind     `json:"kind"`
	Segment  string   `json:"segment"`
	MediaSeq uint64   `json:"media_seq"`
	// SourceOffset is the offset inside the originating manifest/plain
	// text at which the problem was located (0 when not applicable).
	SourceOffset int64 `json:"source_offset"`
	// ByteOffset is the original byte offset inside the input segment.
	ByteOffset int64 `json:"byte_offset"`
	// SafeBoundary is the first byte that cannot be parsed safely after
	// the fault; -1 when no boundary is implied.
	SafeBoundary int64  `json:"safe_boundary"`
	Message      string `json:"message"`
}

// Log collects records in encounter order and answers summary queries.
type Log struct {
	records []Record
}

// NewLog returns an empty log.
func NewLog() *Log { return &Log{} }

// Add appends a record and returns it for convenience.
func (l *Log) Add(r Record) Record {
	if r.SafeBoundary == 0 {
		r.SafeBoundary = -1
	}
	l.records = append(l.records, r)
	return r
}

// Records returns a copy of every collected record.
func (l *Log) Records() []Record {
	out := make([]Record, len(l.records))
	copy(out, l.records)
	return out
}

// identity is the dedup key for the same underlying condition.
func (r Record) identity() struct {
	Stage    Stage
	Kind     Kind
	Segment  string
	MediaSeq uint64
	Offset   int64
} {
	return struct {
		Stage    Stage
		Kind     Kind
		Segment  string
		MediaSeq uint64
		Offset   int64
	}{r.Stage, r.Kind, r.Segment, r.MediaSeq, r.SourceOffset}
}

// AddUnique appends a record only when no record with the same stage,
// kind, segment, sequence and source offset already exists. It reports
// whether the record was newly added.
func (l *Log) AddUnique(r Record) bool {
	key := r.identity()
	for _, existing := range l.records {
		if existing.identity() == key {
			return false
		}
	}
	l.Add(r)
	return true
}

// HasError reports whether any error-severity record exists.
func (l *Log) HasError() bool {
	for _, r := range l.records {
		if r.Severity == SeverityError {
			return true
		}
	}
	return false
}

// ByStage returns records produced by the given stage.
func (l *Log) ByStage(s Stage) []Record {
	var out []Record
	for _, r := range l.records {
		if r.Stage == s {
			out = append(out, r)
		}
	}
	return out
}

// ErrorCount counts error-severity records.
func (l *Log) ErrorCount() int {
	n := 0
	for _, r := range l.records {
		if r.Severity == SeverityError {
			n++
		}
	}
	return n
}

// String renders a stable, path-free single-line representation.
func (r Record) String() string {
	return fmt.Sprintf("%s/%s/%s segment=%s seq=%d source_offset=%d byte_offset=%d safe_boundary=%d: %s",
		r.Stage, r.Severity, r.Kind, r.Segment, r.MediaSeq,
		r.SourceOffset, r.ByteOffset, r.SafeBoundary, r.Message)
}
