package timeline

import (
	"mediapack/internal/model"
)

// trackState holds the per-track clock and source-switch bookkeeping.
type trackState struct {
	trackID uint32
	lastRep int64 // last repaired tick (monotonic high-water mark)
	lastSrc string
	lastEnd int64 // last repaired tick + duration
	rtpExt  uint64
	rtpRaw  uint32 // previous raw 32-bit RTP timestamp
	hasRTP  bool
}

// checkpoint is a deterministic restoration point. Replaying stableEvents
// from zero must reproduce boundaries and the watermark exactly.
type checkpoint struct {
	watermark    int64
	tracks       map[uint32]*trackState
	stableEvents []Event
	boundaries   []model.Boundary
	seen         map[string]uint64
}

// Timeline processes events and exposes stable, repaired boundaries.
type Timeline struct {
	rule       Rule
	watermark  int64
	tracks     map[uint32]*trackState
	seen       map[string]uint64 // dedup key -> first Seq
	boundaries []model.Boundary
	// All accepted events in arrival order; checkpoints snapshot prefixes.
	events []Event
	// ReplayBookkeeping.
	checkpoints   []checkpoint
	checkpointGap int
	// Stats surfaced to the report.
	ReplayCount  int `json:"replayCount"`
	WrapCount    int `json:"wrapCount"`
	OverlapCount int `json:"overlapCount"`
	GapCount     int `json:"gapCount"`
}

// New constructs a Timeline with the given rule and checkpoint cadence. A
// gap <= 0 defaults to every 8 stable events.
func New(rule Rule, checkpointGap int) *Timeline {
	if checkpointGap <= 0 {
		checkpointGap = 8
	}
	return &Timeline{
		rule:          rule,
		tracks:        map[uint32]*trackState{},
		seen:          map[string]uint64{},
		checkpointGap: checkpointGap,
	}
}

// Watermark returns the current event-time high-water mark in master ticks.
func (t *Timeline) Watermark() int64 { return t.watermark }

// Boundaries returns the repaired edges already stable (at or before the
// watermark). The slice is a copy so callers cannot mutate internal state.
func (t *Timeline) Boundaries() []model.Boundary {
	out := make([]model.Boundary, len(t.boundaries))
	copy(out, t.boundaries)
	return out
}

func (t *Timeline) state(track uint32) *trackState {
	s := t.tracks[track]
	if s == nil {
		s = &trackState{trackID: track}
		t.tracks[track] = s
	}
	return s
}

// advanceWatermark moves the watermark to the highest repaired tick and, at
// the configured cadence, snapshots a checkpoint for later deterministic replay.
func (t *Timeline) advanceWatermark(rep int64) {
	if rep > t.watermark {
		t.watermark = rep
	}
	if len(t.events)%t.checkpointGap == 0 {
		t.snapshot()
	}
}

func (t *Timeline) snapshot() {
	cp := checkpoint{
		watermark:    t.watermark,
		tracks:       cloneTracks(t.tracks),
		stableEvents: append([]Event(nil), t.events...),
		boundaries:   append([]model.Boundary(nil), t.boundaries...),
		seen:         cloneSeen(t.seen),
	}
	t.checkpoints = append(t.checkpoints, cp)
}

func cloneTracks(in map[uint32]*trackState) map[uint32]*trackState {
	out := make(map[uint32]*trackState, len(in))
	for k, v := range in {
		cp := *v
		out[k] = &cp
	}
	return out
}

func cloneSeen(in map[string]uint64) map[string]uint64 {
	out := make(map[string]uint64, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
