// Error definitions shared by the RTP, codec, MP4 and reporting stages.
package model

import (
	"fmt"
	"sort"
)

// ErrorCode is a stable machine-readable identifier for an investigation
// finding. Codes are part of the report contract and must not be renamed
// without updating the acceptance scripts.
type ErrorCode string

const (
	// RTP-level findings.
	ErrDuplicatePacket  ErrorCode = "duplicate_packet"
	ErrSequenceGap      ErrorCode = "sequence_gap"
	ErrSequenceWrap     ErrorCode = "sequence_wrap"
	ErrReorderExceeded  ErrorCode = "reorder_window_exceeded"
	ErrRTPMalformed     ErrorCode = "rtp_malformed"
	ErrTimestampRegress ErrorCode = "timestamp_regression"

	// Codec-level findings.
	ErrTruncatedPacket  ErrorCode = "truncated_packet"
	ErrInvalidDuration  ErrorCode = "invalid_packet_duration"
	ErrUnknownPayload   ErrorCode = "unknown_payload"
	ErrUnsupportedCodec ErrorCode = "unsupported_codec"
	ErrEmptyPayload     ErrorCode = "empty_payload"

	// MP4-level findings.
	ErrMissingIndex     ErrorCode = "missing_index"
	ErrInvalidTimestamp ErrorCode = "invalid_timestamp"
	ErrBrokenChunk      ErrorCode = "broken_chunk_offset"
	ErrUnknownTrack     ErrorCode = "unknown_track"
	ErrNoKeyframes      ErrorCode = "no_keyframes"
	ErrKeyframeGap      ErrorCode = "keyframe_gap"

	// Container-level findings.
	ErrVolatileTimeline ErrorCode = "volatile_timeline"
	ErrInputRejected    ErrorCode = "input_rejected"
	ErrCanceled         ErrorCode = "canceled"
)

// Severity describes how strongly a finding affects the investigation.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

// Source identifies which analysis path produced a finding.
type Source string

const (
	SourceRTP   Source = "rtp"
	SourceCodec Source = "codec"
	SourceMP4   Source = "mp4"
	SourceFlow  Source = "flow"
	SourceMerge Source = "merge"
)

// Finding is a structured error record. It always carries enough context to
// re-run the analysis and locate the damaged region: the source, the media
// span in ticks, and the packet or track identifier when one exists.
type Finding struct {
	Code     ErrorCode `json:"code"`
	Severity Severity  `json:"severity"`
	Source   Source    `json:"source"`
	Message  string    `json:"message"`
	Track    uint32    `json:"track,omitempty"`
	Stream   uint32    `json:"stream,omitempty"`
	Sequence *uint32   `json:"sequence,omitempty"`
	Span     Span      `json:"span"`
	Detail   string    `json:"detail,omitempty"`
}

// NewFinding builds a finding with the given code and a fixed span.
func NewFinding(code ErrorCode, src Source, sev Severity, span Span, format string, args ...any) Finding {
	return Finding{
		Code:     code,
		Severity: sev,
		Source:   src,
		Message:  fmt.Sprintf(format, args...),
		Span:     span,
	}
}

// SpanOf returns the finding span, which is always present.
func (f Finding) SpanOf() Span { return f.Span }

// Findings is a sortable slice of findings.
type Findings []Finding

// SortFindings orders findings by media span, then source, code and sequence
// so reports are stable across runs.
func SortFindings(f Findings) {
	sort.SliceStable(f, func(i, j int) bool {
		a, b := f[i], f[j]
		if a.Span.Start != b.Span.Start {
			return a.Span.Start < b.Span.Start
		}
		if a.Span.End != b.Span.End {
			return a.Span.End < b.Span.End
		}
		if a.Source != b.Source {
			return a.Source < b.Source
		}
		if a.Track != b.Track {
			return a.Track < b.Track
		}
		return sequenceKey(a) < sequenceKey(b)
	})
}

func sequenceKey(f Finding) uint32 {
	if f.Sequence == nil {
		return 0
	}
	return *f.Sequence
}

// CountCode returns how many findings carry the given code.
func CountCode(f Findings, code ErrorCode) int {
	n := 0
	for _, item := range f {
		if item.Code == code {
			n++
		}
	}
	return n
}

// CountSeverity returns how many findings carry the given severity.
func CountSeverity(f Findings, sev Severity) int {
	n := 0
	for _, item := range f {
		if item.Severity == sev {
			n++
		}
	}
	return n
}

// MaxSeverity returns the strongest severity present, or SeverityInfo when
// the list is empty.
func MaxSeverity(f Findings) Severity {
	best := SeverityInfo
	for _, item := range f {
		if severityRank(item.Severity) > severityRank(best) {
			best = item.Severity
		}
	}
	return best
}

func severityRank(s Severity) int {
	switch s {
	case SeverityError:
		return 2
	case SeverityWarning:
		return 1
	default:
		return 0
	}
}
