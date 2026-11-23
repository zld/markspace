package timeline

import (
	"sort"

	"medfix/internal/clock"
)

// Aligner repairs multi-track timelines: it maps every track's original
// timescale onto the unified tick scale, applies anchor/drift rules,
// de-duplicates repeated events by (source offset, content hash) and marks
// cross-track conflicts that exceed the configured tolerance.
type Aligner struct {
	cfg       Config
	tracks    map[string]Track
	byTrack   map[string][]Event
	rules     map[string][]RepairRule
	baseTS    map[string]int64 // first accepted raw timestamp per track
	scales    map[string]uint32
	seen      map[string]bool // dedup key: track|offset|hash
	conflicts []Error
}

// AlignmentResult is the repaired timeline handed to the index subsystem.
type AlignmentResult struct {
	Events           []Event
	Tracks           []Track
	Rules            []RepairRule
	Conflicts        []Error
	NoKeyframeTracks []string
}

// NewAligner creates an aligner with the given configuration.
func NewAligner(cfg Config) *Aligner {
	return &Aligner{
		cfg:     cfg,
		tracks:  map[string]Track{},
		byTrack: map[string][]Event{},
		rules:   map[string][]RepairRule{},
		baseTS:  map[string]int64{},
		scales:  map[string]uint32{},
		seen:    map[string]bool{},
	}
}

// AddTrack registers a track with its original timescale preserved.
func (a *Aligner) AddTrack(t Track) {
	a.tracks[t.ID] = t
	a.scales[t.ID] = t.Timescale
}

// AddEvent ingests one parsed event. Duplicates are marked and dropped from
// the repaired output but remain discoverable via Deduped events on rerun.
func (a *Aligner) AddEvent(ev Event) error {
	if _, ok := a.scales[ev.TrackID]; !ok {
		return NewError(CodeValidation, "transform", ev.TrackID, ev.Offset.Begin,
			"event for unregistered track", nil)
	}
	key := dedupKey(ev)
	if a.seen[key] {
		ev.Deduped = true
		ev.RuleID = a.ruleID(ev.TrackID, "dedup")
	}
	a.seen[key] = true
	if _, ok := a.baseTS[ev.TrackID]; !ok {
		a.baseTS[ev.TrackID] = ev.RawTS
	}
	a.byTrack[ev.TrackID] = append(a.byTrack[ev.TrackID], ev)
	return nil
}

func dedupKey(ev Event) string {
	return ev.TrackID + "|" + itoa(ev.Offset.Begin) + "|" + ev.ContentHash
}

// Align runs the full repair and returns the sorted, conflict-annotated
// timeline. When cross-track conflicts exceed tolerance the result carries
// both the marked events and the conflict errors; callers must refuse to
// publish an index in that state.
func (a *Aligner) Align() (*AlignmentResult, error) {
	res := &AlignmentResult{}

	for id, tr := range a.tracks {
		events := a.byTrack[id]
		sort.SliceStable(events, func(i, j int) bool {
			if events[i].RawTS == events[j].RawTS {
				return events[i].Seq < events[j].Seq
			}
			return events[i].RawTS < events[j].RawTS
		})
		scale := a.scales[id]
		base := a.baseTS[id]
		rule := a.makeAnchorRule(id, base)
		a.rules[id] = []RepairRule{rule}
		slope := clock.NewRational(a.cfg.DriftSlopeNum, a.cfg.DriftSlopeDen)

		out := make([]Event, 0, len(events))
		var prevTick int64 = -1
		for i := range events {
			ev := events[i]
			if ev.Deduped {
				continue // duplicates are removed from the repaired timeline
			}
			ticks := a.repairTick(ev, base, scale, slope, rule)
			ev.Ticks = ticks
			ev.EndTicks = ticks + clock.ToTicks(deltaTo(events, i), scale)
			ev.RuleID = rule.ID
			if ticks < prevTick {
				ev.Conflict = true
				a.conflicts = append(a.conflicts, *NewError(
					CodeTimescaleConflict, "transform", id, ev.Offset.Begin,
					"non-monotonic repaired timestamp", nil))
			}
			prevTick = ticks
			out = append(out, ev)
		}
		a.byTrack[id] = out
		if !tr.IsKeyframe && !hasAnyKeyframe(out) {
			tr.NoKeyframe = true
			a.tracks[id] = tr
			res.NoKeyframeTracks = append(res.NoKeyframeTracks, id)
		}
	}

	a.markCrossTrackConflicts()

	trackIDs := make([]string, 0, len(a.tracks))
	for id := range a.tracks {
		trackIDs = append(trackIDs, id)
	}
	sort.Strings(trackIDs)
	for _, id := range trackIDs {
		res.Tracks = append(res.Tracks, a.tracks[id])
	}
	// Merge per-track events in deterministic track order; the final stable
	// sort below then orders by tick, with fully deterministic tie-breakers,
	// so the serialized index hash is identical across reruns.
	for _, id := range trackIDs {
		res.Events = append(res.Events, a.byTrack[id]...)
	}
	sort.SliceStable(res.Events, func(i, j int) bool {
		if res.Events[i].Ticks != res.Events[j].Ticks {
			return res.Events[i].Ticks < res.Events[j].Ticks
		}
		if res.Events[i].TrackID != res.Events[j].TrackID {
			return res.Events[i].TrackID < res.Events[j].TrackID
		}
		return res.Events[i].Seq < res.Events[j].Seq
	})
	for _, id := range trackIDs {
		res.Rules = append(res.Rules, a.rules[id]...)
	}
	res.Conflicts = a.conflicts
	sort.SliceStable(res.Conflicts, func(i, j int) bool {
		if res.Conflicts[i].TrackID != res.Conflicts[j].TrackID {
			return res.Conflicts[i].TrackID < res.Conflicts[j].TrackID
		}
		return res.Conflicts[i].Offset < res.Conflicts[j].Offset
	})
	if len(res.Conflicts) > 0 {
		return res, ErrConflict
	}
	return res, nil
}

// repairTick converts one raw timestamp into repaired unified ticks:
// anchor subtraction, exact timescale conversion, then integer drift slope.
func (a *Aligner) repairTick(ev Event, base int64, scale uint32, slope clock.Rational, rule RepairRule) int64 {
	rel := ev.RawTS - base
	ticks := clock.ToTicks(rel, scale)
	if !slope.Equal(clock.NewRational(1, 1)) {
		scaled := slope.Mul(clock.NewRational(ticks, 1))
		ticks = scaled.Round().Int64()
	}
	return ticks + rule.OffsetTick
}

func (a *Aligner) makeAnchorRule(trackID string, base int64) RepairRule {
	return RepairRule{
		ID: a.ruleID(trackID, "anchor-offset"), TrackID: trackID,
		Kind: "anchor-offset", AnchorSeq: 0, AnchorTick: 0,
		OffsetTick: 0, SlopeNum: a.cfg.DriftSlopeNum, SlopeDen: a.cfg.DriftSlopeDen,
		Reason: "first accepted event anchored to tick zero",
	}
}

func (a *Aligner) ruleID(trackID, kind string) string {
	return "rule-" + kind + "-" + trackID
}

func hasAnyKeyframe(evs []Event) bool {
	for _, ev := range evs {
		if ev.Keyframe {
			return true
		}
	}
	return false
}

func deltaTo(evs []Event, i int) int64 {
	if i+1 >= len(evs) {
		return 0
	}
	d := evs[i+1].RawTS - evs[i].RawTS
	if d < 0 {
		return 0
	}
	return d
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
