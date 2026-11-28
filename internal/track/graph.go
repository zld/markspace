package track

import (
	"fmt"
	"sort"

	"mediaregress/internal/media"
)

// Node is one processing stage inside a single processing lane. A lane is a
// track plus its dependent steps; dependencies may cross lanes (for example
// silence detection waits for the timeline of the same track).
type Node string

const (
	NodeParse       Node = "parse"
	NodeTimeline    Node = "timeline"
	NodeSplit       Node = "split"
	NodeSilence     Node = "silence"
	NodeFingerprint Node = "fingerprint"
)

// State is the lifecycle of a node.
type State string

const (
	StatePending   State = "pending"
	StateReady     State = "ready"
	StateRunning   State = "running"
	StateCompleted State = "completed"
	StateFailed    State = "failed"
	StateSkipped   State = "skipped"
)

// Item is a node instance keyed by track ID. Global nodes use the empty track.
type Item struct {
	Track string
	Node  Node
}

func (i Item) String() string {
	if i.Track == "" {
		return string(i.Node)
	}
	return i.Track + "/" + string(i.Node)
}

// Graph is a DAG of processing nodes. It is deliberately small: the pipeline
// stages are fixed, but per-track fan-out and dependency gating are explicit.
type Graph struct {
	nodes map[Item]*node
	order []Item
}

type node struct {
	state      State
	err        error
	deps       []Item
	dependents []Item
	stage      media.Stage
}

// NewGraph creates an empty graph.
func NewGraph() *Graph {
	return &Graph{nodes: map[Item]*node{}}
}

// AddNode registers a node with its prerequisites. Dependencies must already
// exist; registering a node twice is an error rather than a silent overwrite.
func (g *Graph) AddNode(it Item, stage media.Stage, deps ...Item) error {
	if _, exists := g.nodes[it]; exists {
		return fmt.Errorf("graph: duplicate node %s", it)
	}
	n := &node{state: StatePending, stage: stage}
	seen := map[Item]bool{}
	for _, d := range deps {
		if d == it {
			return fmt.Errorf("graph: node %s depends on itself", it)
		}
		if _, ok := g.nodes[d]; !ok {
			return fmt.Errorf("graph: node %s depends on missing %s", it, d)
		}
		if seen[d] {
			continue
		}
		seen[d] = true
		n.deps = append(n.deps, d)
		g.nodes[d].dependents = append(g.nodes[d].dependents, it)
	}
	g.nodes[it] = n
	g.order = append(g.order, it)
	// A node registered after its dependents were completed is immediately ready.
	if len(n.deps) == 0 {
		n.state = StateReady
	}
	return nil
}

// Has reports whether the node exists.
func (g *Graph) Has(it Item) bool { _, ok := g.nodes[it]; return ok }

// State returns the node state, or StateSkipped for unknown nodes.
func (g *Graph) State(it Item) State {
	if n, ok := g.nodes[it]; ok {
		return n.state
	}
	return StateSkipped
}

// NodeError returns the recorded failure, if any.
func (g *Graph) NodeError(it Item) error {
	if n, ok := g.nodes[it]; ok {
		return n.err
	}
	return nil
}

// Ready checks that every prerequisite completed. It never mutates state.
func (g *Graph) Ready(it Item) error {
	n, ok := g.nodes[it]
	if !ok {
		return media.Errorf(media.ClassDependency, media.StageProvenance, it.Track, -1, "unknown graph node %s", it)
	}
	var missing []Item
	for _, d := range n.deps {
		if g.nodes[d].state != StateCompleted {
			missing = append(missing, d)
		}
	}
	if len(missing) > 0 {
		sort.Slice(missing, func(i, j int) bool { return missing[i].String() < missing[j].String() })
		return media.Errorf(media.ClassDependency, n.stage, it.Track, -1,
			"node %s blocked by unfinished %v", it, missing)
	}
	if n.state == StateFailed {
		return media.Errorf(media.ClassDependency, n.stage, it.Track, -1, "node %s previously failed", it)
	}
	return nil
}

// Begin marks a ready node running. Callers must check Ready first; Begin
// re-checks so downstream work cannot start out of order.
func (g *Graph) Begin(it Item) error {
	if err := g.Ready(it); err != nil {
		return err
	}
	g.nodes[it].state = StateRunning
	return nil
}

// Complete marks a running node completed and unblocks dependents.
func (g *Graph) Complete(it Item) error {
	n, ok := g.nodes[it]
	if !ok {
		return fmt.Errorf("graph: unknown node %s", it)
	}
	if n.state != StateRunning && n.state != StateReady {
		return fmt.Errorf("graph: node %s cannot complete from state %s", it, n.state)
	}
	n.state = StateCompleted
	for _, d := range n.dependents {
		if g.nodes[d].state == StatePending && g.allDepsDone(d) {
			g.nodes[d].state = StateReady
		}
	}
	return nil
}

// Fail marks a node failed and transitively skips its dependents.
func (g *Graph) Fail(it Item, err error) {
	n, ok := g.nodes[it]
	if !ok {
		return
	}
	n.state = StateFailed
	n.err = err
	g.skipDependents(it)
}

func (g *Graph) skipDependents(it Item) {
	for _, d := range g.nodes[it].dependents {
		dn := g.nodes[d]
		if dn.state == StateFailed || dn.state == StateSkipped {
			continue
		}
		dn.state = StateSkipped
		g.skipDependents(d)
	}
}

// Skip marks a node skipped without a failure (for example a rejected track).
func (g *Graph) Skip(it Item) {
	n, ok := g.nodes[it]
	if !ok {
		return
	}
	n.state = StateSkipped
	g.skipDependents(it)
}

func (g *Graph) allDepsDone(it Item) bool {
	for _, d := range g.nodes[it].deps {
		if g.nodes[d].state != StateCompleted {
			return false
		}
	}
	return true
}

// Order returns node registration order for deterministic reporting.
func (g *Graph) Order() []Item { return append([]Item(nil), g.order...) }

// CompletedNodes returns completed nodes in registration order.
func (g *Graph) CompletedNodes() []Item {
	var out []Item
	for _, it := range g.order {
		if g.nodes[it].state == StateCompleted {
			out = append(out, it)
		}
	}
	return out
}

// PendingTracks returns, per track, the nodes that have not completed. It
// drives checkpoint resume: only incomplete work is re-executed.
func (g *Graph) PendingTracks() map[string][]Node {
	out := map[string][]Node{}
	for _, it := range g.order {
		if it.Track == "" || g.nodes[it].state == StateCompleted {
			continue
		}
		out[it.Track] = append(out[it.Track], it.Node)
	}
	return out
}

// BuildTrackLanes wires the standard per-track pipeline for every accepted
// track. It returns an error when the set of
// tracks is empty so callers cannot silently run downstream stages on nothing.
func BuildTrackLanes(tracks []*Track) (*Graph, error) {
	g := NewGraph()
	if len(tracks) == 0 {
		return nil, media.NewError(media.ClassDependency, media.StageTimeline, "", -1, "no tracks to process")
	}
	for _, t := range tracks {
		parse := Item{Track: t.ID, Node: NodeParse}
		tl := Item{Track: t.ID, Node: NodeTimeline}
		sp := Item{Track: t.ID, Node: NodeSplit}
		sil := Item{Track: t.ID, Node: NodeSilence}
		fp := Item{Track: t.ID, Node: NodeFingerprint}
		if err := g.AddNode(parse, media.StageParse); err != nil {
			return nil, err
		}
		if t.Rejected {
			g.Skip(parse)
			continue
		}
		if err := g.AddNode(tl, media.StageTimeline, parse); err != nil {
			return nil, err
		}
		if err := g.AddNode(sp, media.StageSplit, tl); err != nil {
			return nil, err
		}
		if t.Kind == KindAudio {
			if err := g.AddNode(sil, media.StageSilence, tl); err != nil {
				return nil, err
			}
		} else {
			g.Skip(sil)
		}
		// Fingerprints need a stable segment list and, for audio, the silence
		// verdict, so audio fingerprints wait for both.
		if t.Kind == KindAudio {
			if err := g.AddNode(fp, media.StageFingerprint, sp, sil); err != nil {
				return nil, err
			}
		} else if err := g.AddNode(fp, media.StageFingerprint, sp); err != nil {
			return nil, err
		}
	}
	return g, nil
}
