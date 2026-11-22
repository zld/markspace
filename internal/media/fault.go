package media

import (
	"fmt"
	"sort"
	"strings"
)

// RecoveryClass states how far recovery may proceed after a fault.
type RecoveryClass uint8

const (
	// Recoverable faults let the affected layer skip the damaged region and
	// resynchronize at the next verified keyframe.
	Recoverable RecoveryClass = iota + 1
	// Unrecoverable faults force a retained gap: no content may be invented.
	Unrecoverable
	// Informational marks a verified condition worth auditing, not damage.
	Informational
)

func (c RecoveryClass) String() string {
	switch c {
	case Recoverable:
		return "recoverable"
	case Unrecoverable:
		return "unrecoverable"
	case Informational:
		return "informational"
	default:
		return "unknown"
	}
}

// FaultKind is a stable machine-readable fault category.
type FaultKind string

const (
	KindStartCodeSplit   FaultKind = "start_code_split"
	KindNALTruncated     FaultKind = "nal_truncated"
	KindOBUTruncated     FaultKind = "obu_truncated"
	KindIndexOutOfRange  FaultKind = "index_out_of_range"
	KindIndexDuplicate   FaultKind = "index_duplicate_offset"
	KindIndexRegress     FaultKind = "index_time_regress"
	KindKeyframeRegress  FaultKind = "keyframe_time_regress"
	KindIntervalExceeded FaultKind = "keyframe_interval_exceeded"
	KindNonMonotonic     FaultKind = "non_monotonic_time"
	KindBoundaryDrift    FaultKind = "boundary_drift"
	KindBoundaryConflict FaultKind = "boundary_conflict"
	KindGap              FaultKind = "recovery_gap"
	KindLayerUnusable    FaultKind = "layer_unusable"
)

// Fault carries the location and disposition of one detected defect.
// ByteOffset is -1 when the defect has no meaningful byte position.
type Fault struct {
	Kind       FaultKind
	Layer      string
	Detail     string
	ByteOffset int64
	HasSpan    bool
	Span       TickInterval
	Class      RecoveryClass
}

// NewFault builds a fault without a time span.
func NewFault(kind FaultKind, layer, detail string, offset int64, class RecoveryClass) Fault {
	return Fault{Kind: kind, Layer: layer, Detail: detail, ByteOffset: offset, Class: class}
}

// WithSpan attaches the time span affected by the fault.
func (f Fault) WithSpan(span TickInterval) Fault {
	f.HasSpan = true
	f.Span = span
	return f
}

// Code returns the kind as a stable string.
func (f Fault) Code() string { return string(f.Kind) }

// String renders one auditable fault line.
func (f Fault) String() string {
	var b strings.Builder
	b.WriteString(string(f.Kind))
	b.WriteString(" layer=")
	if f.Layer == "" {
		b.WriteString("-")
	} else {
		b.WriteString(f.Layer)
	}
	if f.ByteOffset >= 0 {
		fmt.Fprintf(&b, " offset=%d", f.ByteOffset)
	}
	if f.HasSpan {
		fmt.Fprintf(&b, " span=%s", f.Span.String())
	}
	fmt.Fprintf(&b, " class=%s", f.Class)
	if f.Detail != "" {
		b.WriteString(" detail=")
		b.WriteString(f.Detail)
	}
	return b.String()
}

// SortFaults orders faults deterministically: layer, byte offset, kind, detail.
func SortFaults(fs []Fault) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		if a.Layer != b.Layer {
			return a.Layer < b.Layer
		}
		if a.ByteOffset != b.ByteOffset {
			if a.ByteOffset < 0 {
				return false
			}
			if b.ByteOffset < 0 {
				return true
			}
			return a.ByteOffset < b.ByteOffset
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Detail < b.Detail
	})
}

// CountByKind returns stable kind->count pairs for reporting.
func CountByKind(fs []Fault) []KindCount {
	counts := map[FaultKind]int{}
	for _, f := range fs {
		counts[f.Kind]++
	}
	out := make([]KindCount, 0, len(counts))
	for k, v := range counts {
		out = append(out, KindCount{Kind: k, Count: v})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// KindCount is one row of a fault summary.
type KindCount struct {
	Kind  FaultKind
	Count int
}

// FaultSummary is a deterministic, human-readable aggregate.
func FaultSummary(fs []Fault) string {
	rows := CountByKind(fs)
	parts := make([]string, 0, len(rows))
	for _, r := range rows {
		parts = append(parts, fmt.Sprintf("%s=%d", r.Kind, r.Count))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ",")
}
