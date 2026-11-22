package dag

import (
	"encoding/json"
	"os"
	"path/filepath"

	"archivist/internal/model"
)

// Snapshot is the on-disk resume state. Its JSON form is the durable record a
// later run consults to skip already-committed nodes.
type Snapshot struct {
	Committed map[string]CommittedArtifact `json:"committed"`
	Order     []string                     `json:"order"`
}

// SaveSnapshot writes the run's commit ledger atomically next to the manifest.
func (r *Run) SaveSnapshot(dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	snap := Snapshot{Committed: map[string]CommittedArtifact{}, Order: r.graph.Order()}
	r.mu.Lock()
	for k, v := range r.committed {
		snap.Committed[k] = v
	}
	r.mu.Unlock()
	final := filepath.Join(dir, "dag-state.json")
	tmp := final + ".tmp"
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp, final); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return final, nil
}

// LoadSnapshot reads a previously persisted ledger. A missing file is not an
// error; it means this is a fresh run.
func LoadSnapshot(dir string) (*Snapshot, bool, error) {
	path := filepath.Join(dir, "dag-state.json")
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, false, err
	}
	return &snap, true, nil
}

// Restore marks every node whose idempotency key appears in the snapshot as
// already committed, so a resumed run never double-commits an artifact.
func (r *Run) Restore(snap *Snapshot) int {
	restored := 0
	for _, id := range r.graph.Order() {
		node := r.graph.Node(id)
		key := node.IdempotencyKey()
		if art, ok := snap.Committed[key]; ok {
			r.mu.Lock()
			r.committed[key] = art
			r.mu.Unlock()
			restored++
		}
	}
	return restored
}

// Blocker identifies the first dependency that prevents a given node from
// running. It walks the node's transitive dependencies in topological order
// and returns the earliest failed or blocked one.
func (r *Run) Blocker(nodeID string) (*NodeResult, error) {
	node := r.graph.Node(nodeID)
	if node == nil {
		return nil, model.AtTrack(model.StageDAG, model.ErrUnsupportedBox, 0,
			"unknown node "+nodeID)
	}
	for _, depID := range r.graph.Order() {
		if !r.isDependency(nodeID, depID) {
			continue
		}
		res := r.results[depID]
		if res != nil && (res.State == StateFailed || res.State == StateBlocked) {
			return res, nil
		}
	}
	return nil, nil
}

// isDependency reports whether maybeDep is a transitive dependency of nodeID.
func (r *Run) isDependency(nodeID, maybeDep string) bool {
	visited := map[string]bool{}
	var walk func(string) bool
	walk = func(cur string) bool {
		if cur == maybeDep {
			return true
		}
		if visited[cur] {
			return false
		}
		visited[cur] = true
		for _, dep := range r.graph.Node(cur).Deps {
			if walk(dep) {
				return true
			}
		}
		return false
	}
	for _, dep := range r.graph.Node(nodeID).Deps {
		if walk(dep) {
			return true
		}
	}
	return false
}

// Rerunnable returns the nodes that can be retried after a crash: nodes that
// never produced a committed artifact. Already-committed nodes are excluded so
// re-execution cannot duplicate outputs.
func (r *Run) Rerunnable() []string {
	var out []string
	for _, id := range r.graph.Order() {
		node := r.graph.Node(id)
		key := node.IdempotencyKey()
		r.mu.Lock()
		_, done := r.committed[key]
		r.mu.Unlock()
		if !done {
			out = append(out, id)
		}
	}
	return out
}

// DownstreamUnrun lists every node that did not execute because it depended
// on the first blocker. It is the precise blast radius of a permanent failure.
func (rep *Report) DownstreamUnrun(graph *Graph) []string {
	if rep.FirstBlocker == nil {
		return nil
	}
	return graph.Dependents(rep.FirstBlocker.NodeID)
}
