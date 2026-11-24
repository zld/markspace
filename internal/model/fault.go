// Package model defines the locatable fault model and the stable result
// types shared by every mediascan module.
package model

import "errors"

// ErrPathOutsideMaterial is returned when an input escapes the allowed root.
var ErrPathOutsideMaterial = errors.New("path outside material directory")

// UnifiedTimescale is the single integer tick scale every source timescale
// is mapped to: 1,000,000 ticks per second (microsecond granularity).
const UnifiedTimescale uint32 = 1_000_000

// Severity classifies how the pipeline should treat a Fault.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Stable fault classification codes. Repeated runs on the same input must
// produce byte-identical codes.
const (
	CodeZeroTimescale         = "zero_timescale"
	CodeTickOverflow          = "tick_overflow"
	CodeManifestParse         = "manifest_parse_error"
	CodePathEscape            = "path_escape"
	CodeIllegalBoxLength      = "illegal_box_length"
	CodeTruncatedBox          = "truncated_box"
	CodeMissingInit           = "missing_initialization"
	CodeMissingSampleTable    = "missing_sample_table"
	CodeDashAlignment         = "dash_representation_misaligned"
	CodeWrapEvidence          = "wrap_evidence_insufficient"
	CodeCheckpointCorrupt     = "checkpoint_corrupt"
	CodeSegmentUnreadable     = "segment_unreadable"
	CodeTimelineDiscontinuity = "timeline_discontinuity"
)

// Unknown marks offsets, sample indices and times that cannot be determined.
const Unknown int64 = -1

// Fault is a locatable, serializable description of a problem found while
// analysing an input. All spatial and temporal coordinates are explicit so
// downstream tooling can jump to the exact byte range or media interval.
type Fault struct {
	// Code is one of the Code* constants.
	Code string `json:"code"`
	// Severity is either SeverityError or SeverityWarning.
	Severity string `json:"severity"`
	// Message is a human readable, deterministic explanation.
	Message string `json:"message"`
	// Segment is the manifest-relative URI of the offending segment.
	Segment string `json:"segment"`
	// BoxType is the four-character MP4 box type when applicable.
	BoxType string `json:"box_type"`
	// OffsetBytes is the best single byte offset locating the fault:
	// EOF for truncation, box start for illegal lengths, sample start otherwise.
	OffsetBytes int64 `json:"fault_offset_bytes"`
	// BoxOffset is the absolute file offset of the relevant box header.
	BoxOffset int64 `json:"box_offset_bytes"`
	// TimeStart/TimeEnd bound the affected media interval in unified ticks.
	TimeStart int64 `json:"fault_time_start_ticks"`
	TimeEnd   int64 `json:"fault_time_end_ticks"`
	// SampleIndex is the zero-based sample associated with the fault.
	SampleIndex int64 `json:"sample_index"`
}

// PrimaryOffset returns OffsetBytes, falling back to BoxOffset and Unknown.
func (f Fault) PrimaryOffset() int64 {
	if f.OffsetBytes >= 0 {
		return f.OffsetBytes
	}
	if f.BoxOffset >= 0 {
		return f.BoxOffset
	}
	return Unknown
}

// PrimaryTime returns the start of the affected interval when known.
func (f Fault) PrimaryTime() int64 {
	if f.TimeStart >= 0 {
		return f.TimeStart
	}
	return Unknown
}

// NewFault builds an error-severity Fault with all coordinates unknown.
func NewFault(code, segment, message string) Fault {
	return Fault{
		Code:        code,
		Severity:    SeverityError,
		Message:     message,
		Segment:     segment,
		OffsetBytes: Unknown,
		BoxOffset:   Unknown,
		TimeStart:   Unknown,
		TimeEnd:     Unknown,
		SampleIndex: Unknown,
	}
}

// NewWarning builds a warning-severity Fault.
func NewWarning(code, segment, message string) Fault {
	f := NewFault(code, segment, message)
	f.Severity = SeverityWarning
	return f
}

// HasErrors reports whether the fault list contains error-severity entries.
func HasErrors(faults []Fault) bool {
	for i := range faults {
		if faults[i].Severity == SeverityError {
			return true
		}
	}
	return false
}
