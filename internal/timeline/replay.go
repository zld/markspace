package timeline

import (
	"mediatimeline/internal/container"
)

// ReplayLog buffers late events and serves them back in deterministic order.
// Dedup uses the stable (track, sample) identity, so a retransmitted or
// replayed sample is counted exactly once regardless of how often it arrives.
type ReplayLog struct {
	seen     map[identity]struct{}
	late     []container.SampleEvent
	replayed uint64
	skipped  uint64
}

type identity struct {
	TrackID uint32 `json:"track_id"`
	Sample  int64  `json:"sample"`
}

// NewReplayLog creates an empty replay log.
func NewReplayLog() *ReplayLog {
	return &ReplayLog{seen: make(map[identity]struct{})}
}

// Admit registers an event the first time its identity is seen. It returns
// false for duplicates, which must never re-enter the output.
func (r *ReplayLog) Admit(e container.SampleEvent) bool {
	id := identity{TrackID: e.TrackID, Sample: e.SampleIndex}
	if _, ok := r.seen[id]; ok {
		r.skipped++
		return false
	}
	r.seen[id] = struct{}{}
	return true
}

// AddLate stores a late event for later replay, admitting its identity first.
// Duplicates are rejected.
func (r *ReplayLog) AddLate(e container.SampleEvent) bool {
	if !r.Admit(e) {
		return false
	}
	r.late = append(r.late, e)
	return true
}

// StoreLate buffers an event whose identity was already admitted by an earlier
// Admit call. It never touches the identity set again.
func (r *ReplayLog) StoreLate(e container.SampleEvent) {
	r.late = append(r.late, e)
}

// Drain returns all buffered late events in stable replay order and clears the
// buffer. The same input always yields the same ordering and contents.
func (r *ReplayLog) Drain() []container.SampleEvent {
	StableSort(r.late)
	out := r.late
	r.late = nil
	r.replayed += uint64(len(out))
	return out
}

// Pending reports how many late events are buffered.
func (r *ReplayLog) Pending() int { return len(r.late) }

// ReplayedCount reports events returned by Drain across the log's lifetime.
func (r *ReplayLog) ReplayedCount() uint64 { return r.replayed }

// SkippedCount reports duplicate admissions that were suppressed.
func (r *ReplayLog) SkippedCount() uint64 { return r.skipped }

// ReplaySnapshot is the restartable replay state.
type ReplaySnapshot struct {
	Seen     []identity
	Late     []container.SampleEvent
	Replayed uint64
	Skipped  uint64
}

// Snapshot captures replay state for a checkpoint.
func (r *ReplayLog) Snapshot() ReplaySnapshot {
	seen := make([]identity, 0, len(r.seen))
	for id := range r.seen {
		seen = append(seen, id)
	}
	late := append([]container.SampleEvent(nil), r.late...)
	return ReplaySnapshot{seen, late, r.replayed, r.skipped}
}

// Restore rehydrates replay state from a checkpoint.
func (r *ReplayLog) Restore(s ReplaySnapshot) {
	r.seen = make(map[identity]struct{}, len(s.Seen))
	for _, id := range s.Seen {
		r.seen[id] = struct{}{}
	}
	r.late = append([]container.SampleEvent(nil), s.Late...)
	r.replayed, r.skipped = s.Replayed, s.Skipped
}
