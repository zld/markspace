// Package merge deterministically combines events from the RTP/codec path
// and the MP4 path onto one ordered media timeline. Ordering is total and
// stable: media start, then end, kind, track, stream, sequence and the
// original input order, so identical inputs always produce the same report.
package merge

import (
	"sort"

	"tracecheck/internal/model"
)

// Merge returns a new slice containing all events in a total deterministic
// order. The input slices are not modified; equal events keep their relative
// input order thanks to a stable sort with an explicit Order tiebreak.
func Merge(groups ...[]model.TimelineEvent) []model.TimelineEvent {
	total := 0
	for _, g := range groups {
		total += len(g)
	}
	if total == 0 {
		return nil
	}
	merged := make([]model.TimelineEvent, 0, total)
	for _, g := range groups {
		merged = append(merged, g...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		return merged[i].Less(merged[j])
	})
	// Canonicalize the tiebreak so identical event sets serialize to
	// identical output regardless of the order groups were passed in.
	for i := range merged {
		merged[i].Order = i
	}
	return merged
}

// Monotonic reports whether every event starts at or after the previous
// event's start, which is the timeline invariant checked by the acceptance
// tests after merging both analysis paths.
func Monotonic(events []model.TimelineEvent) bool {
	for i := 1; i < len(events); i++ {
		if events[i].Start < events[i-1].Start {
			return false
		}
	}
	return true
}

// Anomaly is a located time range where packet loss and/or throughput
// changes indicate abnormal transmission. It is the merge-stage view used
// by the reporter; it never reinterprets bandwidth changes as media
// durations.
type Anomaly struct {
	Span        model.Span `json:"span"`
	LossCount   int        `json:"loss_count"`
	FlowChanges int        `json:"flow_changes"`
	Reasons     []string   `json:"reasons"`
}

// MergeAnomalies unions overlapping anomaly spans so one damaged region is
// reported once even when loss and flow evidence arrive separately.
func MergeAnomalies(in []Anomaly) []Anomaly {
	if len(in) == 0 {
		return nil
	}
	sorted := append([]Anomaly(nil), in...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Span.Start != sorted[j].Span.Start {
			return sorted[i].Span.Start < sorted[j].Span.Start
		}
		return sorted[i].Span.End < sorted[j].Span.End
	})
	out := []Anomaly{sorted[0]}
	for _, a := range sorted[1:] {
		last := &out[len(out)-1]
		if a.Span.Start <= last.Span.End {
			if a.Span.End > last.Span.End {
				last.Span.End = a.Span.End
			}
			last.LossCount += a.LossCount
			last.FlowChanges += a.FlowChanges
			last.Reasons = append(last.Reasons, a.Reasons...)
			continue
		}
		out = append(out, a)
	}
	return out
}
