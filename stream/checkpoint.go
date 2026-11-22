package stream

import (
	"sort"
	"sync"

	"medpipe/timeline"
)

// MemCheckpoint is the default Checkpointer: an in-memory, concurrency-safe
// store keyed by track id. The CLI adapter persists the same values to disk.
type MemCheckpoint struct {
	mu   sync.Mutex
	data map[string]timeline.Checkpoint
}

// NewMemCheckpoint builds an empty checkpoint store.
func NewMemCheckpoint() *MemCheckpoint {
	return &MemCheckpoint{data: make(map[string]timeline.Checkpoint)}
}

// Load returns the committed checkpoint of a track.
func (m *MemCheckpoint) Load(trackID string) (timeline.Checkpoint, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp, ok := m.data[trackID]
	return cp, ok
}

// Commit records a checkpoint. Commits must advance monotonically per track;
// an older position is rejected so a replayed block cannot roll back recovery.
func (m *MemCheckpoint) Commit(cp timeline.Checkpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if prev, ok := m.data[cp.TrackID]; ok && cp.Position < prev.Position {
		return timeline.NewError(timeline.ErrCodeCheckpoint, timeline.StageRunner, cp.TrackID,
			"checkpoint regression %d -> %d", prev.Position, cp.Position)
	}
	m.data[cp.TrackID] = cp
	return nil
}

// All returns committed checkpoints ordered by track id for stable manifests.
func (m *MemCheckpoint) All() []timeline.Checkpoint {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.data))
	for id := range m.data {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	out := make([]timeline.Checkpoint, 0, len(ids))
	for _, id := range ids {
		out = append(out, m.data[id])
	}
	return out
}
