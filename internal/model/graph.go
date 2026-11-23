package model

import (
	"fmt"
	"sort"
	"strconv"
)

// ConflictDecision records how one metadata conflict was resolved. Decisions
// are part of the evidence trail so remux output can be re-checked.
type ConflictDecision struct {
	Kind     ErrorKind `json:"kind"`
	TrackID  uint32    `json:"track_id"`
	Policy   Policy    `json:"policy"`
	Detail   string    `json:"detail"`
}

// TopoOrder returns track IDs in dependency order: every track appears after
// all tracks it depends on. Video tracks are additionally ordered before
// audio tracks when no explicit edge exists, because audio alignment needs a
// confirmed video boundary. A cycle produces a CyclicDependency-style error
// naming the offending chain.
func (g *Graph) TopoOrder() ([]uint32, error) {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[uint32]int, len(g.order))
	var stack []uint32
	var order []uint32

	// Deterministic iteration: source order, but video before audio only as
	// a tiebreak so identical inputs always produce identical plans.
	roots := append([]uint32(nil), g.order...)
	sort.SliceStable(roots, func(i, j int) bool {
		ti, tj := g.Tracks[roots[i]], g.Tracks[roots[j]]
		vi := ti.Kind == KindVideo
		vj := tj.Kind == KindVideo
		if vi != vj {
			return vi
		}
		return roots[i] < roots[j]
	})

	var visit func(id uint32) error
	visit = func(id uint32) error {
		switch color[id] {
		case black:
			return nil
		case gray:
			chain := append(append([]uint32(nil), stack...), id)
			return &CycleError{Chain: chain}
		}
		t, ok := g.Tracks[id]
		if !ok {
			return &MissingDependencyError{Track: id}
		}
		color[id] = gray
		stack = append(stack, id)
		deps := append([]uint32(nil), t.DependsOn...)
		sort.SliceStable(deps, func(a, b int) bool { return deps[a] < deps[b] })
		for _, dep := range deps {
			if err := visit(dep); err != nil {
				return err
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		order = append(order, id)
		return nil
	}
	for _, id := range roots {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return order, nil
}

// ValidateEdges checks that every dependency points at an existing track and
// that the 32-track limit is respected.
func (g *Graph) ValidateEdges() error {
	if len(g.order) > MaxTracks {
		return &LimitError{What: "tracks", Got: len(g.order), Max: MaxTracks}
	}
	for _, id := range g.order {
		t := g.Tracks[id]
		seen := make(map[uint32]struct{}, len(t.DependsOn))
		for _, dep := range t.DependsOn {
			if dep == id {
				return &CycleError{Chain: []uint32{id, id}}
			}
			if _, ok := g.Tracks[dep]; !ok {
				return &MissingDependencyError{Track: id, Missing: dep}
			}
			if _, dup := seen[dep]; dup {
				continue
			}
			seen[dep] = struct{}{}
		}
	}
	return nil
}

// CycleError names the dependency chain that closes a loop.
type CycleError struct{ Chain []uint32 }

func (e *CycleError) Error() string {
	return "cyclic track dependency: " + u32List(e.Chain)
}

// MediaError adapts the cycle into the shared error model.
func (e *CycleError) MediaError() MediaError {
	return MediaError{
		Kind:     KindCyclicDependency,
		Severity: SeverityError,
		Detail:   e.Error(),
	}
}

// MissingDependencyError flags an edge to an unknown track id.
type MissingDependencyError struct {
	Track   uint32
	Missing uint32
}

func (e *MissingDependencyError) Error() string {
	if e.Missing == 0 {
		return "track " + u32(e.Track) + " does not exist"
	}
	return "track " + u32(e.Track) + " depends on missing track " + u32(e.Missing)
}

func u32(v uint32) string { return strconv.FormatUint(uint64(v), 10) }

func u32List(vs []uint32) string {
	out := ""
	for i, v := range vs {
		if i > 0 {
			out += " -> "
		}
		out += u32(v)
	}
	return out
}

func fmtLimit(what string, got, max int) string {
	return fmt.Sprintf("%s limit exceeded: got %d, max %d", what, got, max)
}
