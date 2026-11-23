package model

import (
	"errors"
	"fmt"
	"sort"
)

// Code is a stable, machine-readable error category. Codes are part of the
// audit contract: they are written to reports and checkpoints verbatim.
type Code string

const (
	// Manifest errors (exit code 2).
	CodeManifestSyntax       Code = "manifest_syntax"
	CodeManifestMissingField Code = "manifest_missing_field"
	CodeManifestBadRange     Code = "manifest_bad_range"
	CodeManifestTimebase     Code = "manifest_timebase"
	CodeManifestBoundary     Code = "manifest_boundary"

	// Transport errors.
	CodeTransportShortRead  Code = "transport_short_read"
	CodeTransportSequence   Code = "transport_sequence"
	CodeTransportDuplicate  Code = "transport_duplicate"
	CodeTransportGap        Code = "transport_gap"
	CodeTransportTimestamp  Code = "transport_timestamp"
	CodeTransportBadPayload Code = "transport_bad_payload"

	// Inspection errors (exit code 2 or 3 depending on Location).
	CodeMP4BoxTruncated  Code = "mp4_box_truncated"
	CodeMP4BadLength     Code = "mp4_bad_box_length"
	CodeMP4SampleTable   Code = "mp4_sample_table_truncated"
	CodeMP4NonMonotonic  Code = "mp4_non_monotonic_timestamp"
	CodeMP4TrackTimebase Code = "mp4_track_timebase"
	CodeMP4Structure     Code = "mp4_structure"

	// Damage located in media time (exit code 3).
	CodeDamageGap            Code = "damage_gap"
	CodeDamageTruncation     Code = "damage_truncation"
	CodeDamageTimestamp      Code = "damage_timestamp"
	CodeDamageRepresentation Code = "damage_representation"

	// Scheduler and checkpoint errors (exit code 4).
	CodeDependencyBlocked   Code = "dependency_blocked"
	CodeCheckpointCorrupt   Code = "checkpoint_corrupt"
	CodeCheckpointMismatch  Code = "checkpoint_mismatch"
	CodeCheckpointVersion   Code = "checkpoint_version"
	CodeIdempotencyConflict Code = "idempotency_conflict"

	// Generic input handling.
	CodeInputMissing Code = "input_missing"
	CodeInputBadArgs Code = "input_bad_args"
)

// Severity ranks how a fault affects the recoverable output.
type Severity int

const (
	SeverityInfo Severity = iota
	SeverityWarn
	SeverityError
)

// MarshalJSON renders severities as stable lowercase strings.
func (s Severity) MarshalJSON() ([]byte, error) {
	switch s {
	case SeverityWarn:
		return []byte(`"warn"`), nil
	case SeverityError:
		return []byte(`"error"`), nil
	default:
		return []byte(`"info"`), nil
	}
}

// UnmarshalJSON parses the severities emitted by MarshalJSON.
func (s *Severity) UnmarshalJSON(b []byte) error {
	switch string(b) {
	case `"info"`:
		*s = SeverityInfo
	case `"warn"`:
		*s = SeverityWarn
	case `"error"`:
		*s = SeverityError
	default:
		return fmt.Errorf("model: unknown severity %s", string(b))
	}
	return nil
}

// String returns a stable lowercase label.
func (s Severity) String() string {
	switch s {
	case SeverityWarn:
		return "warn"
	case SeverityError:
		return "error"
	default:
		return "info"
	}
}

// Source identifies where a fault was detected. Segment is always meaningful;
// the remaining fields are populated by whichever layer localised the fault.
type Source struct {
	// Layer is one of "manifest", "transport", "inspect", "scheduler",
	// "checkpoint", "pipeline".
	Layer string `json:"layer"`
	// File is a manifest or fragment name relative to the fixture root.
	File string `json:"file,omitempty"`
	// Segment is the playlist segment identifier (index or "$Number").
	Segment string `json:"segment,omitempty"`
	// Representation is the DASH representation ID or HLS variant ID.
	Representation string `json:"representation,omitempty"`
	// Offset is a byte offset inside the named file, -1 when not applicable.
	Offset int64 `json:"offset"`
	// Bytes is the number of bytes involved in the fault, or 0.
	Bytes int64 `json:"bytes,omitempty"`
	// Line and Column are 1-based source positions for text manifests.
	Line   int `json:"line,omitempty"`
	Column int `json:"column,omitempty"`
}

// Location says whether a fault is a media-time interval, a byte interval, or
// both. Interval boundaries are inclusive start and exclusive end.
type Location struct {
	// MediaStart and MediaEnd are ticks on the shared timeline. Valid when
	// MediaSet is true.
	MediaStart int64 `json:"media_start"`
	MediaEnd   int64 `json:"media_end"`
	MediaSet   bool  `json:"media_set"`
	// Timebase is the shared timebase of the media interval.
	Timebase int64 `json:"timebase,omitempty"`
	// ByteStart and ByteEnd are absolute offsets in Source.File. Valid when
	// ByteSet is true.
	ByteStart int64 `json:"byte_start"`
	ByteEnd   int64 `json:"byte_end"`
	ByteSet   bool  `json:"byte_set"`
}

// Fault is the single cross-module error carrier. Every module returns faults
// with at least a code and a source; damage found in media adds a Location.
type Fault struct {
	Code     Code     `json:"code"`
	Severity Severity `json:"severity"`
	Message  string   `json:"message"`
	Source   Source   `json:"source"`
	Location Location `json:"location"`
	// Cause is an optional wrapped error, never serialized.
	Cause error `json:"-"`
}

// NewFault builds a fault with a code, message and source.
func NewFault(code Code, sev Severity, src Source, format string, args ...any) *Fault {
	if src.Offset == 0 && src.Layer != "manifest" {
		src.Offset = -1
	}
	return &Fault{Code: code, Severity: sev, Message: fmt.Sprintf(format, args...), Source: src}
}

// Error implements error. It always reports the segment and either the byte
// offset or the media time so logs and reports are self-contained.
func (f *Fault) Error() string {
	if f == nil {
		return "<nil fault>"
	}
	loc := ""
	switch {
	case f.Location.MediaSet:
		loc = fmt.Sprintf(" media=[%d,%d)/%d", f.Location.MediaStart, f.Location.MediaEnd, f.Location.Timebase)
	case f.Location.ByteSet:
		loc = fmt.Sprintf(" bytes=[%d,%d)", f.Location.ByteStart, f.Location.ByteEnd)
	case f.Source.Offset >= 0:
		loc = fmt.Sprintf(" offset=%d", f.Source.Offset)
	}
	seg := f.Source.Segment
	if seg == "" {
		seg = "-"
	}
	where := fmt.Sprintf("layer=%s segment=%s", f.Source.Layer, seg)
	if f.Source.File != "" {
		where += " file=" + f.Source.File
	}
	if f.Source.Line > 0 {
		where += fmt.Sprintf(" line=%d col=%d", f.Source.Line, f.Source.Column)
	}
	return fmt.Sprintf("%s: %s: %s%s", f.Code, where, f.Message, loc)
}

// Unwrap exposes the wrapped cause.
func (f *Fault) Unwrap() error { return f.Cause }

// WithCause attaches a low-level error.
func (f *Fault) WithCause(err error) *Fault {
	f.Cause = err
	return f
}

// WithMedia records a media-time interval.
func (f *Fault) WithMedia(start, end, timebase int64) *Fault {
	f.Location.MediaStart, f.Location.MediaEnd = start, end
	f.Location.Timebase = timebase
	f.Location.MediaSet = true
	return f
}

// WithBytes records a byte interval.
func (f *Fault) WithBytes(start, end int64) *Fault {
	f.Location.ByteStart, f.Location.ByteEnd = start, end
	f.Location.ByteSet = true
	return f
}

// WithSegment sets the segment identifier.
func (f *Fault) WithSegment(seg string) *Fault {
	f.Source.Segment = seg
	return f
}

// AsFault extracts a *Fault from err.
func AsFault(err error) (*Fault, bool) {
	var f *Fault
	if errors.As(err, &f) {
		return f, true
	}
	return nil, false
}

// FaultList is an ordered, de-duplicated collection of faults. It serializes
// deterministically: sorted by layer, segment, offset, then code.
type FaultList struct {
	items []*Fault
}

// Add appends a fault, ignoring nil.
func (l *FaultList) Add(f *Fault) {
	if f != nil {
		l.items = append(l.items, f)
	}
}

// AddErr appends err if it is a fault.
func (l *FaultList) AddErr(err error) bool {
	if f, ok := AsFault(err); ok {
		l.Add(f)
		return true
	}
	return false
}

// Len returns the number of faults.
func (l *FaultList) Len() int { return len(l.items) }

// All returns the faults sorted deterministically and de-duplicated.
func (l *FaultList) All() []*Fault {
	out := make([]*Fault, len(l.items))
	copy(out, l.items)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Source.Layer != b.Source.Layer {
			return a.Source.Layer < b.Source.Layer
		}
		if a.Source.Segment != b.Source.Segment {
			return a.Source.Segment < b.Source.Segment
		}
		if a.Source.Offset != b.Source.Offset {
			return a.Source.Offset < b.Source.Offset
		}
		if a.Location.MediaSet != b.Location.MediaSet {
			return !a.Location.MediaSet
		}
		if a.Location.MediaStart != b.Location.MediaStart {
			return a.Location.MediaStart < b.Location.MediaStart
		}
		if a.Location.ByteSet != b.Location.ByteSet {
			return !a.Location.ByteSet
		}
		if a.Location.ByteStart != b.Location.ByteStart {
			return a.Location.ByteStart < b.Location.ByteStart
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
	dedup := out[:0]
	for _, f := range out {
		if len(dedup) > 0 && sameFault(dedup[len(dedup)-1], f) {
			continue
		}
		dedup = append(dedup, f)
	}
	return dedup
}

// First returns the most severe fault, or nil. Ties break on the sorted order.
func (l *FaultList) First() *Fault {
	all := l.All()
	var best *Fault
	for _, f := range all {
		if best == nil || f.Severity > best.Severity {
			best = f
		}
	}
	return best
}

func sameFault(a, b *Fault) bool {
	return a.Code == b.Code && a.Source == b.Source && a.Location == b.Location && a.Message == b.Message
}
