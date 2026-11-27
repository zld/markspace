// Package timeline merges client events, indexed media samples and subtitle
// cues onto one nanosecond timeline.
//
// Merging never shifts an item's time. Each item keeps the instant it was
// observed at, plus the deviation from its track's reference clock; grouping
// and skew accounting are derived views. A group whose largest cross-track
// deviation exceeds the budget is marked over-threshold, and its items stay at
// their original instants.
package timeline

import (
	"fmt"
	"sort"

	"media.rebuild/internal/model"
)

// ThresholdNs is the maximum tolerated cross-track deviation (40 ms).
const ThresholdNs int64 = 40_000_000

// Kind names the origin of an item.
type Kind string

const (
	KindEvent    Kind = "event"
	KindMedia    Kind = "media"
	KindSubtitle Kind = "subtitle"
)

// Item is one normalized entry on the merged timeline.
type Item struct {
	ID        string `json:"id"`
	Kind      Kind   `json:"kind"`
	Track     string `json:"track"`
	Source    string `json:"source"`
	Line      int    `json:"line,omitempty"`
	TimeNs    int64  `json:"time_ns"`
	EndNs     int64  `json:"end_ns,omitempty"`
	RefTimeNs int64  `json:"ref_time_ns"`
	SkewNs    int64  `json:"skew_ns"`
	AbsSkewNs int64  `json:"abs_skew_ns"`
	Label     string `json:"label,omitempty"`
	Over      bool   `json:"over_budget"`
	Group     string `json:"group"`
}

// Group is a set of items that should describe the same instant.
type Group struct {
	ID        string   `json:"id"`
	TimeNs    int64    `json:"time_ns"`
	Items     []string `json:"items"`
	MaxSkewNs int64    `json:"max_skew_ns"`
	Over      bool     `json:"over_budget"`
}

// AnomalyRange is a contiguous span of the timeline containing at least one
// over-budget group, with the worst deviation seen inside it.
type AnomalyRange struct {
	GroupIDs  []string `json:"group_ids"`
	StartNs   int64    `json:"start_ns"`
	EndNs     int64    `json:"end_ns"`
	MaxSkewNs int64    `json:"max_skew_ns"`
	Ranges    []string `json:"sources"`
}

// Result is the merged timeline.
type Result struct {
	Items       []Item         `json:"items"`
	Groups      []Group        `json:"groups"`
	Anomalies   []AnomalyRange `json:"anomalies"`
	MaxSkewNs   int64          `json:"max_skew_ns"`
	OverBudget  int            `json:"over_budget"`
	ThresholdNs int64          `json:"threshold_ns"`
}

// Event is a client-side playback event.
type Event struct {
	ID     string
	Track  string
	Source string
	Line   int
	TimeNs int64
	Label  string
	// MediaTimeNs, when non-zero, is the media clock the client reported at
	// this event; it is the reference for the media track in the same group.
	MediaTimeNs int64
}

// Option configures merging.
type Option func(*mergeConfig)

type mergeConfig struct {
	thresholdNs      int64
	mediaToleranceNs int64
}

// WithThreshold overrides the 40 ms budget.
func WithThreshold(ns int64) Option { return func(c *mergeConfig) { c.thresholdNs = ns } }

// WithMediaTolerance sets how far a media sample may be from a group's event
// before it is treated as ungrouped. Defaults to half the group window.
func WithMediaTolerance(ns int64) Option { return func(c *mergeConfig) { c.mediaToleranceNs = ns } }

// Merge builds the timeline.
//
// Grouping: each client event anchors a group at its media time (or event time
// when no media time was reported). A subtitle cue joins the group whose
// anchor lies within its interval. A media sample joins the nearest group
// within the media tolerance. Every item's skew is its own time minus its
// group's anchor time, so an over-budget group pins the deviation to the
// offending tracks without moving anything.
func Merge(events []Event, samples []SampleRef, cues []CueRef, opts ...Option) (*Result, error) {
	cfg := mergeConfig{thresholdNs: ThresholdNs, mediaToleranceNs: 20_000_000}
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.thresholdNs <= 0 {
		return nil, model.Newf(model.CodeInvalidArgument, model.Source{Offset: -1}, "threshold must be positive")
	}

	res := &Result{ThresholdNs: cfg.thresholdNs}
	sortedEvents := append([]Event(nil), events...)
	sort.SliceStable(sortedEvents, func(i, j int) bool {
		if sortedEvents[i].TimeNs != sortedEvents[j].TimeNs {
			return sortedEvents[i].TimeNs < sortedEvents[j].TimeNs
		}
		return sortedEvents[i].ID < sortedEvents[j].ID
	})

	anchors := make([]int64, len(sortedEvents))
	for i, e := range sortedEvents {
		a := e.TimeNs
		if e.MediaTimeNs != 0 {
			a = e.MediaTimeNs
		}
		anchors[i] = a
		res.Items = append(res.Items, Item{
			ID: e.ID, Kind: KindEvent, Track: e.Track, Source: e.Source, Line: e.Line,
			TimeNs: e.TimeNs, RefTimeNs: a, Label: e.Label, Group: groupID(i),
		})
	}

	sortedSamples := append([]SampleRef(nil), samples...)
	sort.SliceStable(sortedSamples, func(i, j int) bool {
		if sortedSamples[i].TimeNs != sortedSamples[j].TimeNs {
			return sortedSamples[i].TimeNs < sortedSamples[j].TimeNs
		}
		return sortedSamples[i].ID < sortedSamples[j].ID
	})
	for _, s := range sortedSamples {
		gi, ok := nearestGroup(anchors, s.TimeNs, cfg.mediaToleranceNs)
		group := "unassigned"
		var ref int64
		if ok {
			group = groupID(gi)
			ref = anchors[gi]
		} else {
			ref = s.TimeNs
		}
		res.Items = append(res.Items, Item{
			ID: s.ID, Kind: KindMedia, Track: s.Track, Source: s.Source,
			TimeNs: s.TimeNs, RefTimeNs: ref, Label: s.Label, Group: group,
		})
	}

	sortedCues := append([]CueRef(nil), cues...)
	sort.SliceStable(sortedCues, func(i, j int) bool {
		if sortedCues[i].TimeNs != sortedCues[j].TimeNs {
			return sortedCues[i].TimeNs < sortedCues[j].TimeNs
		}
		return sortedCues[i].ID < sortedCues[j].ID
	})
	for _, c := range sortedCues {
		gi, ok := containingGroup(anchors, c.TimeNs, c.EndNs)
		group := "unassigned"
		var ref int64
		if ok {
			group = groupID(gi)
			ref = anchors[gi]
		} else {
			ref = c.TimeNs
		}
		res.Items = append(res.Items, Item{
			ID: c.ID, Kind: KindSubtitle, Track: c.Track, Source: c.Source,
			Line: c.Line, TimeNs: c.TimeNs, EndNs: c.EndNs, RefTimeNs: ref,
			Label: c.Label, Group: group,
		})
	}

	// A media sample inside a group at the same instant is the group itself,
	// so only deviations of a track from the anchor matter. Compute skews and
	// then roll up groups in stable ID order.
	for i := range res.Items {
		it := &res.Items[i]
		it.SkewNs = it.TimeNs - it.RefTimeNs
		if it.SkewNs < 0 {
			it.AbsSkewNs = -it.SkewNs
		} else {
			it.AbsSkewNs = it.SkewNs
		}
		if it.AbsSkewNs > cfg.thresholdNs {
			it.Over = true
		}
	}

	groups := make(map[string]*Group)
	var order []string
	for i := range res.Items {
		it := &res.Items[i]
		g, ok := groups[it.Group]
		if !ok {
			g = &Group{ID: it.Group, TimeNs: it.RefTimeNs}
			groups[it.Group] = g
			order = append(order, it.Group)
		}
		g.Items = append(g.Items, it.ID)
		if it.AbsSkewNs > g.MaxSkewNs {
			g.MaxSkewNs = it.AbsSkewNs
		}
		if it.AbsSkewNs > res.MaxSkewNs {
			res.MaxSkewNs = it.AbsSkewNs
		}
	}
	sort.Strings(order)
	for _, id := range order {
		g := groups[id]
		sort.Strings(g.Items)
		g.Over = g.MaxSkewNs > cfg.thresholdNs
		if g.Over {
			res.OverBudget++
		}
		res.Groups = append(res.Groups, *g)
	}
	res.Anomalies = anomalyRanges(res.Groups)

	sort.SliceStable(res.Items, func(i, j int) bool {
		a, b := res.Items[i], res.Items[j]
		if a.TimeNs != b.TimeNs {
			return a.TimeNs < b.TimeNs
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	})
	return res, nil
}

func groupID(i int) string { return fmt.Sprintf("g%04d", i) }

// nearestGroup returns the index of the anchor closest to t within tolerance.
func nearestGroup(anchors []int64, t, tol int64) (int, bool) {
	if len(anchors) == 0 {
		return 0, false
	}
	i := sort.Search(len(anchors), func(i int) bool { return anchors[i] >= t })
	best := -1
	bestDist := int64(-1)
	consider := func(j int) {
		if j < 0 || j >= len(anchors) {
			return
		}
		d := anchors[j] - t
		if d < 0 {
			d = -d
		}
		if d <= tol && (best < 0 || d < bestDist) {
			best, bestDist = j, d
		}
	}
	consider(i)
	consider(i - 1)
	return best, best >= 0
}

// containingGroup returns the index of an anchor inside [start,end].
func containingGroup(anchors []int64, start, end int64) (int, bool) {
	if len(anchors) == 0 || end < start {
		return 0, false
	}
	i := sort.Search(len(anchors), func(i int) bool { return anchors[i] >= start })
	if i < len(anchors) && anchors[i] <= end {
		return i, true
	}
	// A cue without a reported end but anchored exactly is still a member.
	return 0, false
}

// anomalyRanges coalesces consecutive over-budget groups into ranges. Two
// groups join a range when no in-budget group sits between them.
func anomalyRanges(groups []Group) []AnomalyRange {
	var out []AnomalyRange
	var cur *AnomalyRange
	var lastIdx = -2
	for i, g := range groups {
		if !g.Over {
			continue
		}
		if cur != nil && lastIdx == i-1 {
			cur.GroupIDs = append(cur.GroupIDs, g.ID)
			if g.MaxSkewNs > cur.MaxSkewNs {
				cur.MaxSkewNs = g.MaxSkewNs
			}
			if g.TimeNs > cur.EndNs {
				cur.EndNs = g.TimeNs
			}
			lastIdx = i
			continue
		}
		if cur != nil {
			out = append(out, *cur)
		}
		cur = &AnomalyRange{GroupIDs: []string{g.ID}, StartNs: g.TimeNs, EndNs: g.TimeNs, MaxSkewNs: g.MaxSkewNs}
		lastIdx = i
	}
	if cur != nil {
		out = append(out, *cur)
	}
	for i := range out {
		out[i].Ranges = append([]string(nil), out[i].GroupIDs...)
	}
	return out
}

// SampleRef is a media sample viewed as a timeline item.
type SampleRef struct {
	ID     string
	Track  string
	Source string
	TimeNs int64
	Label  string
}

// CueRef is a subtitle cue viewed as a timeline item.
type CueRef struct {
	ID     string
	Track  string
	Source string
	Line   int
	TimeNs int64
	EndNs  int64
	Label  string
}
