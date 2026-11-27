// Package timeline merges timed events from every reconstructed track and
// transport source into one canonical time line: events are deduplicated by
// their stable task key, same-tick collisions are ordered deterministically
// (track id, then original sequence number) and each ground-truth plan item
// is aligned with what was actually observed.
package timeline

import (
	"sort"

	"regress/internal/clock"
	"regress/internal/model"
)

// Occurrence is one observed emission of an event. A repeated publish keeps
// the same Key but appears as a separate Occurrence.
type Occurrence struct {
	Event model.Event
	// Source identifies where the occurrence came from ("mp4:<track>" or
	// "rtp:<track>"), which keeps the stable sort total.
	Source string
}

// Alignment binds one planned item to its observed occurrences.
type Alignment struct {
	Plan        model.PlanItem
	Occurrences []Occurrence
	// ObservedTick is the presentation tick of the first (ordered)
	// occurrence, or zero when the item was not observed at all.
	ObservedTick clock.Tick
	Missing      bool
	// Duplicate is true when more than one occurrence shares the key.
	Duplicate bool
}

// Timeline is the merged, deduplicated and plan-aligned result.
type Timeline struct {
	// Occurrences is every observed emission in deterministic order:
	// ascending observed tick, then track id, then sequence, then source.
	Occurrences []Occurrence
	// Unique holds one Event per stable key, in the same deterministic
	// order as its first occurrence.
	Unique []model.Event
	// Alignment is keyed by plan item key and kept in plan order.
	Alignment []Alignment
}

type sourceEvents struct {
	source string
	events []model.Event
}

// Build merges MP4 tracks and RTP transport streams into one Timeline.
// mp4Tracks and rtpStreams may be nil; every event they contain is ingested
// with an explicit source tag so equal timestamps never make ordering depend
// on map iteration or slice address.
func Build(mp4Tracks []model.Track, rtpStreams []SourceStream,
	plan []model.PlanItem) *Timeline {
	var groups []sourceEvents
	for i := range mp4Tracks {
		tr := mp4Tracks[i]
		if len(tr.Events) > 0 {
			groups = append(groups, sourceEvents{
				source: "mp4:" + tr.ID,
				events: append([]model.Event(nil), tr.Events...),
			})
		}
	}
	for _, st := range rtpStreams {
		if len(st.Events) > 0 {
			groups = append(groups, sourceEvents{
				source: "rtp:" + st.TrackID,
				events: append([]model.Event(nil), st.Events...),
			})
		}
	}

	var occ []Occurrence
	for _, g := range groups {
		for _, ev := range g.events {
			occ = append(occ, Occurrence{Event: ev, Source: g.source})
		}
	}
	sort.SliceStable(occ, func(i, j int) bool {
		a, b := occ[i].Event, occ[j].Event
		if a.ObservedTick != b.ObservedTick {
			return a.ObservedTick < b.ObservedTick
		}
		if a.Track != b.Track {
			return a.Track < b.Track
		}
		if a.Seq != b.Seq {
			return a.Seq < b.Seq
		}
		return occ[i].Source < occ[j].Source
	})

	seen := map[string]bool{}
	var unique []model.Event
	for _, o := range occ {
		if !seen[o.Event.Key] {
			seen[o.Event.Key] = true
			unique = append(unique, o.Event)
		}
	}

	tl := &Timeline{Occurrences: occ, Unique: unique}
	tl.align(plan)
	return tl
}

// align joins the plan with observed occurrences. Lookups use a map but the
// result is emitted in the plan's own order for deterministic output.
func (t *Timeline) align(plan []model.PlanItem) {
	byKey := map[string][]Occurrence{}
	for _, o := range t.Occurrences {
		byKey[o.Event.Key] = append(byKey[o.Event.Key], o)
	}
	for _, item := range plan {
		a := Alignment{Plan: item, Occurrences: byKey[item.Key]}
		switch {
		case len(a.Occurrences) == 0:
			a.Missing = true
		default:
			a.ObservedTick = a.Occurrences[0].Event.ObservedTick
			a.Duplicate = len(a.Occurrences) > 1
		}
		t.Alignment = append(t.Alignment, a)
	}
}

// Lookup returns the unique event for a stable key, or false when absent.
func (t *Timeline) Lookup(key string) (model.Event, bool) {
	for _, ev := range t.Unique {
		if ev.Key == key {
			return ev, true
		}
	}
	return model.Event{}, false
}
