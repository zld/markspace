// Package fault defines the single error and anomaly vocabulary shared by
// parsing, slicing and auditing. Every subsystem records failures as an
// Anomaly so that one ordered manifest explains what happened at every
// bitrate layer, at what input byte offset, over which media time range.
package fault

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"replaykit/media/clock"
)

// Category names the kind of defect or recovery observed.
type Category string

const (
	// Container defects.
	CategoryTruncatedBox   Category = "truncated_box"
	CategoryBoxOverflow    Category = "box_overflow"
	CategorySampleOverflow Category = "sample_out_of_range"
	CategoryMalformedBox   Category = "malformed_box"

	// Index defects.
	CategoryIndexIncomplete Category = "index_incomplete"
	CategoryIndexChecksum   Category = "index_checksum"
	CategoryIndexRebuilt    Category = "index_rebuilt"

	// Packet defects.
	CategoryPacketLost      Category = "packet_lost"
	CategoryPacketDuplicate Category = "packet_duplicate"
	CategoryPacketLate      Category = "packet_late"
	CategoryPacketMalformed Category = "packet_malformed"
	CategoryAudioDuration   Category = "audio_duration_invalid"

	// Boundary defects.
	CategoryBoundarySkew   Category = "boundary_skew"
	CategorySourceCorrupt  Category = "source_corrupt_skip"
	CategoryUncoveredRange Category = "range_uncovered"

	// Output defects.
	CategoryHashMismatch   Category = "hash_mismatch"
	CategorySegmentRebuilt Category = "segment_rebuilt"
	CategorySegmentReused  Category = "segment_reused"
	CategorySegmentPartial Category = "segment_partial"
	CategorySegmentFail    Category = "segment_failed"
)

// Severity ranks an anomaly from informational to task-fatal.
type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
	SeverityFatal   Severity = "fatal"
)

func (s Severity) rank() int {
	switch s {
	case SeverityInfo:
		return 0
	case SeverityWarning:
		return 1
	case SeverityError:
		return 2
	case SeverityFatal:
		return 3
	default:
		return 4
	}
}

// Action describes what the pipeline did in response to a defect.
type Action string

const (
	ActionNone         Action = "none"
	ActionSkipped      Action = "skipped"
	ActionRebuiltIndex Action = "rebuilt_index"
	ActionResynced     Action = "resynced_to_keyframe"
	ActionReordered    Action = "reordered"
	ActionDropped      Action = "dropped_duplicate"
	ActionReused       Action = "reused_output"
	ActionRegenerated  Action = "regenerated_output"
	ActionMarkedPart   Action = "marked_partial"
)

// Range is an inclusive-start, exclusive-end media range in one layer's ticks.
type Range struct {
	Start clock.Tick `json:"start"`
	End   clock.Tick `json:"end"`
}

// IsEmpty reports whether the range has no extent.
func (r Range) IsEmpty() bool { return r.End.Value <= r.Start.Value }

// Duration is the range length in ticks.
func (r Range) Duration() int64 { return r.End.Value - r.Start.Value }

// Anomaly is one manifest line. The fields are deliberately flat so that
// JSON output stays stable and diffable.
type Anomaly struct {
	Sequence   int      `json:"seq"`
	Layer      string   `json:"layer"`
	Offset     int64    `json:"input_offset"`
	Category   Category `json:"category"`
	Severity   Severity `json:"severity"`
	Action     Action   `json:"action"`
	Timescale  uint32   `json:"timescale,omitempty"`
	MediaStart int64    `json:"media_start,omitempty"`
	MediaEnd   int64    `json:"media_end,omitempty"`
	Detail     string   `json:"detail,omitempty"`
}

// MediaRange returns the range carried by the anomaly, if any.
func (a Anomaly) MediaRange() Range {
	return Range{
		Start: clock.Tick{Value: a.MediaStart},
		End:   clock.Tick{Value: a.MediaEnd},
	}
}

// New builds an anomaly with an explicit media range.
func New(layer string, offset int64, cat Category, sev Severity, act Action, timescale uint32, r Range, detail string) Anomaly {
	return Anomaly{
		Layer:      layer,
		Offset:     offset,
		Category:   cat,
		Severity:   sev,
		Action:     act,
		Timescale:  timescale,
		MediaStart: r.Start.Value,
		MediaEnd:   r.End.Value,
		Detail:     detail,
	}
}

// FatalError is returned when no usable time mapping or container can be
// established. It names the input range that could not be processed so the
// caller can report it and exit with the fatal status.
type FatalError struct {
	Op     string
	Layer  string
	Offset int64
	Range  Range
	Err    error
}

func (e *FatalError) Error() string {
	var b strings.Builder
	b.WriteString("fatal")
	if e.Op != "" {
		b.WriteString(" [")
		b.WriteString(e.Op)
		b.WriteString("]")
	}
	if e.Layer != "" {
		b.WriteString(" layer=")
		b.WriteString(e.Layer)
	}
	b.WriteString(fmt.Sprintf(" offset=%d range=[%d,%d)", e.Offset, e.Range.Start.Value, e.Range.End.Value))
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *FatalError) Unwrap() error { return e.Err }

// Fatal wraps err as a fatal processing error.
func Fatal(op, layer string, offset int64, r Range, err error) error {
	return &FatalError{Op: op, Layer: layer, Offset: offset, Range: r, Err: err}
}

// IsFatal reports whether err is (or wraps) a FatalError.
func IsFatal(err error) bool {
	var fe *FatalError
	return errors.As(err, &fe)
}

// RecoverableError marks a defect that was localised and worked around.
type RecoverableError struct {
	Category Category
	Layer    string
	Offset   int64
	Range    Range
	Detail   string
}

func (e *RecoverableError) Error() string {
	return fmt.Sprintf("%s layer=%s offset=%d range=[%d,%d) %s",
		e.Category, e.Layer, e.Offset, e.Range.Start.Value, e.Range.End.Value, e.Detail)
}

// Recoverable wraps a localised defect.
func Recoverable(cat Category, layer string, offset int64, r Range, detail string) error {
	return &RecoverableError{Category: cat, Layer: layer, Offset: offset, Range: r, Detail: detail}
}

// AsRecoverable reports the recoverable payload of err, if any.
func AsRecoverable(err error) (*RecoverableError, bool) {
	var re *RecoverableError
	if errors.As(err, &re) {
		return re, true
	}
	return nil, false
}

// Ledger accumulates anomalies and assigns stable sequence numbers.
type Ledger struct {
	anomalies []Anomaly
	fatal     error
}

// Add records one anomaly.
func (l *Ledger) Add(a Anomaly) {
	l.anomalies = append(l.anomalies, a)
}

// Record a recoverable error as an anomaly.
func (l *Ledger) Record(err error) {
	if re, ok := AsRecoverable(err); ok {
		l.Add(New(re.Layer, re.Offset, re.Category, SeverityWarning, ActionSkipped, 0, re.Range, re.Detail))
	}
}

// SetFatal remembers the task-fatal error.
func (l *Ledger) SetFatal(err error) { l.fatal = err }

// FatalErr returns the recorded task-fatal error.
func (l *Ledger) FatalErr() error { return l.fatal }

// Anomalies returns a deterministic, sequenced copy of the manifest.
func (l *Ledger) Anomalies() []Anomaly {
	out := make([]Anomaly, len(l.anomalies))
	copy(out, l.anomalies)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Layer != b.Layer {
			return a.Layer < b.Layer
		}
		if a.Offset != b.Offset {
			return a.Offset < b.Offset
		}
		if a.MediaStart != b.MediaStart {
			return a.MediaStart < b.MediaStart
		}
		if a.Category != b.Category {
			return a.Category < b.Category
		}
		return a.Detail < b.Detail
	})
	for i := range out {
		out[i].Sequence = i + 1
	}
	return out
}

// CountBySeverity returns how many anomalies have at least sev.
func (l *Ledger) CountBySeverity(sev Severity) int {
	n := 0
	for _, a := range l.anomalies {
		if a.Severity.rank() >= sev.rank() {
			n++
		}
	}
	return n
}

// HighestSeverity returns the worst severity recorded, or SeverityInfo.
func (l *Ledger) HighestSeverity() Severity {
	worst := SeverityInfo
	for _, a := range l.anomalies {
		if a.Severity.rank() > worst.rank() {
			worst = a.Severity
		}
	}
	return worst
}
