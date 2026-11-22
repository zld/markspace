package timeline

// Replayer wraps an Engine and enforces a hard bound on the number of
// recomputations a single late stream may cause, keeping recovery work
// proportional even under bursty packet loss.
type Replayer struct {
	eng      *Engine
	maxDepth int
}

// NewReplayer binds a depth limit to an engine.
func NewReplayer(eng *Engine, maxDepth int) (*Replayer, error) {
	if maxDepth < 0 {
		return nil, NewError("INVALID_CONFIG", "replay depth must be non-negative",
			map[string]any{"max_depth": maxDepth})
	}
	return &Replayer{eng: eng, maxDepth: maxDepth}, nil
}

// Engine exposes the wrapped engine.
func (r *Replayer) Engine() *Engine { return r.eng }

// Ingest forwards an event and converts an exhausted recompute budget
// into a coded REPLAY_DEPTH_EXCEEDED error.
func (r *Replayer) Ingest(ev Event) error {
	if err := r.eng.Push(ev); err != nil {
		return err
	}
	if r.eng.Metrics().ReplayDepth > r.maxDepth {
		return NewError("REPLAY_DEPTH_EXCEEDED",
			"too many bounded recomputations",
			map[string]any{"depth": r.eng.Metrics().ReplayDepth,
				"limit": r.maxDepth})
	}
	return nil
}

// RecoverFrom rebuilds timeline output from a watermark snapshot plus the
// events that arrived after the snapshot was taken. The late slice may be
// in any order: ingestion is canonicalized internally so repeated recovery
// produces the same digest.
func RecoverFrom(snap Snapshot, window Rat, late []Event) (Snapshot, string, error) {
	eng, err := NewEngine(window)
	if err != nil {
		return Snapshot{}, "", err
	}
	eng.Restore(snap)
	ordered := append([]Event(nil), late...)
	sortEvents(ordered)
	for _, ev := range ordered {
		if err := eng.Push(ev); err != nil {
			return Snapshot{}, "", err
		}
	}
	out := eng.Snapshot()
	return out, eng.Digest(), nil
}
