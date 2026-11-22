package track

import (
	"fmt"
	"sort"

	"mediadist/internal/merr"
)

// UnknownPolicy decides what happens to an input track that the mapping does
// not mention.
type UnknownPolicy int

const (
	// UnknownError rejects an unmapped track with a fatal error.
	UnknownError UnknownPolicy = iota
	// UnknownDrop silently (but recorded) drops an unmapped track.
	UnknownDrop
)

// ParseUnknownPolicy maps a CLI string to a policy.
func ParseUnknownPolicy(s string) (UnknownPolicy, error) {
	switch s {
	case "error", "":
		return UnknownError, nil
	case "drop":
		return UnknownDrop, nil
	default:
		return UnknownError, merr.New(merr.StageTrack, "bad_unknown_policy", merr.NoSpan(0), false,
			"unknown-track policy %q must be error or drop", s)
	}
}

// MapEntry is one explicit input-to-output track mapping. OutputID 0 means the
// track is intentionally dropped even when the unknown policy is error.
type MapEntry struct {
	InputID   uint32
	OutputID  uint32
	Drop      bool
	OutputKind Kind
}

// Mapping is the resolved input-to-output track plan.
type Mapping struct {
	// ToOutput maps each retained input track to its output track ID.
	ToOutput map[uint32]uint32
	// ToInput is the inverse direction.
	ToInput map[uint32]uint32
	// Dropped lists input IDs that were dropped, ascending.
	Dropped []uint32
	// Missing lists output IDs that were requested but have no input.
	Missing []uint32
	// Order is the output order following the topological track order.
	Order []uint32
}

// BuildMapping resolves entries against the graph. Entries are validated for
// unknown inputs and duplicate outputs. Unmapped tracks follow the policy.
// The graph must be acyclic; a cycle is reported as a track error.
func BuildMapping(g *Graph, entries []MapEntry, policy UnknownPolicy) (*Mapping, error) {
	order, err := g.TopoSort()
	if err != nil {
		var ce *CycleError
		if IsCycle(err) {
			ce, _ = err.(*CycleError)
			return nil, merr.New(merr.StageTrack, "dependency_cycle", merr.NoSpan(0), false,
				"refusing to map tracks: cycle %v", ce.Cycle)
		}
		return nil, err
	}

	explicit := make(map[uint32]MapEntry, len(entries))
	for _, e := range entries {
		if _, dup := explicit[e.InputID]; dup {
			return nil, merr.New(merr.StageTrack, "duplicate_mapping",
				merr.Span{Stream: 0, Seq: -1, Offset: -1, TrackID: e.InputID}, false,
				"input track %d mapped more than once", e.InputID)
		}
		if _, ok := g.Get(e.InputID); !ok {
			return nil, merr.New(merr.StageTrack, "unknown_input",
				merr.Span{Stream: 0, Seq: -1, Offset: -1, TrackID: e.InputID}, false,
				"mapping references unknown input track %d", e.InputID)
		}
		if !e.Drop && e.OutputID == 0 {
			return nil, merr.New(merr.StageTrack, "zero_output",
				merr.Span{Stream: 0, Seq: -1, Offset: -1, TrackID: e.InputID}, false,
				"input track %d maps to reserved output id 0", e.InputID)
		}
		explicit[e.InputID] = e
	}

	m := &Mapping{ToOutput: map[uint32]uint32{}, ToInput: map[uint32]uint32{}}
	usedOutput := map[uint32]uint32{}

	for _, id := range order {
		e, has := explicit[id]
		if !has {
			if policy == UnknownDrop {
				m.Dropped = append(m.Dropped, id)
				continue
			}
			return nil, merr.New(merr.StageTrack, "unmapped_track",
				merr.Span{Stream: 0, Seq: -1, Offset: -1, TrackID: id}, true,
				"track %d has no mapping and policy is error", id)
		}
		if e.Drop {
			m.Dropped = append(m.Dropped, id)
			delete(explicit, id)
			continue
		}
		if prev, taken := usedOutput[e.OutputID]; taken {
			return nil, merr.New(merr.StageTrack, "duplicate_output",
				merr.Span{Stream: 0, Seq: -1, Offset: -1, TrackID: id}, false,
				"output track %d requested by inputs %d and %d", e.OutputID, prev, id)
		}
		usedOutput[e.OutputID] = id
		m.ToOutput[id] = e.OutputID
		m.ToInput[e.OutputID] = id
		m.Order = append(m.Order, e.OutputID)
		delete(explicit, id)
	}

	// Any residual explicit entry must reference a track that exists; inputs
	// are all covered by the topological order, so this is a defensive check.
	if len(explicit) != 0 {
		var leftover []uint32
		for id := range explicit {
			leftover = append(leftover, id)
		}
		sort.Slice(leftover, func(i, j int) bool { return leftover[i] < leftover[j] })
		return nil, merr.New(merr.StageTrack, "unresolved_mapping", merr.NoSpan(0), false,
			"mapping entries left unresolved: %v", leftover)
	}

	sort.Slice(m.Dropped, func(i, j int) bool { return m.Dropped[i] < m.Dropped[j] })
	return m, nil
}

// RequestedMissing records output IDs the caller expected but did not receive.
// It is separate from BuildMapping so the caller can supply manifest-derived
// expectations after parsing inputs.
func (m *Mapping) RequestedMissing(expected []uint32) {
	present := make(map[uint32]struct{}, len(m.ToInput))
	for id := range m.ToInput {
		present[id] = struct{}{}
	}
	for _, id := range expected {
		if _, ok := present[id]; !ok {
			m.Missing = append(m.Missing, id)
		}
	}
	sort.Slice(m.Missing, func(i, j int) bool { return m.Missing[i] < m.Missing[j] })
}

// OutputFor returns the output ID for an input track.
func (m *Mapping) OutputFor(input uint32) (uint32, bool) {
	v, ok := m.ToOutput[input]
	return v, ok
}

// IsDropped reports whether an input track was dropped.
func (m *Mapping) IsDropped(input uint32) bool {
	for _, id := range m.Dropped {
		if id == input {
			return true
		}
	}
	return false
}

// String renders a deterministic single-line summary for reports.
func (m *Mapping) String() string {
	return fmt.Sprintf("mapping(in=%d out=%d dropped=%v missing=%v)",
		len(m.ToOutput), len(m.ToInput), m.Dropped, m.Missing)
}
