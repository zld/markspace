// Package merge publishes the final media plan from mapped assets and
// owns rollback. Publication is all-or-nothing per run: when a dependency
// cycle or an unresolved conflict would make the result inconsistent, no
// partial merge output is written. Rollback removes the artefacts of
// affected stages and records precisely which stages were rolled back and
// why.
package merge

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"replay/internal/fault"
	"replay/internal/graph"
	"replay/internal/mapasset"
)

// Segment is one published track interval with its source.
type Segment struct {
	Track   string   `json:"track"`
	StartNS int64    `json:"start_ns"`
	EndNS   int64    `json:"end_ns"`
	Version string   `json:"version"`
	Digest  string   `json:"digest"`
	Sources []string `json:"sources,omitempty"`
}

// Plan is the published merge result.
type Plan struct {
	Segments []Segment `json:"segments"`
	// Order is the dependency order used to build the plan.
	Order []string `json:"order"`
	// ConflictsSeen counts quarantined conflicts carried from mapping.
	ConflictsSeen int `json:"conflicts_seen"`
	// MissingSeen counts missing ranges carried from mapping.
	MissingSeen int `json:"missing_seen"`
}

// MergeInput is the per-track mapping output.
type MergeInput struct {
	Track  string
	Result mapasset.Result
}

// Merge builds the plan in dependency order. It writes nothing until the
// whole plan is valid. A cycle in the graph or a track that appears in
// the result without a graph node is a hard failure.
func Merge(g *graph.Graph, inputs []MergeInput) (*Plan, error) {
	if g == nil {
		return nil, fault.New(fault.KindArgument, "merge requires a graph").At("merge")
	}
	if len(inputs) == 0 {
		return nil, fault.New(fault.KindArgument, "merge requires at least one track").At("merge")
	}
	order, err := g.TopologicalOrder()
	if err != nil {
		return nil, err
	}

	byTrack := make(map[string]mapasset.Result, len(inputs))
	for _, in := range inputs {
		if _, ok := g.Track(in.Track); !ok {
			return nil, fault.New(fault.KindMerge, "mapped track is not in the graph").
				At("merge").WithTrack(in.Track)
		}
		byTrack[in.Track] = in.Result
	}

	plan := &Plan{Order: order}
	for _, track := range order {
		res, ok := byTrack[track]
		if !ok {
			// A graph track with no mapping is a missing item, not a
			// silent omission.
			plan.MissingSeen++
			continue
		}
		plan.ConflictsSeen += res.ConflictCount()
		plan.MissingSeen += res.MissingCount()
		for _, slot := range res.Slots {
			seg := Segment{
				Track:   slot.Track,
				StartNS: slot.Interval.Start,
				EndNS:   slot.Interval.End,
				Version: slot.Version,
				Digest:  slot.Digest,
				Sources: append([]string(nil), slot.MergedFrom...),
			}
			plan.Segments = append(plan.Segments, seg)
		}
	}
	sortPlan(plan)
	return plan, nil
}

// sortPlan orders segments by track order, then start, then digest. Track
// order is the dependency order so downstream consumers always see
// upstream tracks first.
func sortPlan(plan *Plan) {
	rank := make(map[string]int, len(plan.Order))
	for i, track := range plan.Order {
		rank[track] = i
	}
	sort.SliceStable(plan.Segments, func(i, j int) bool {
		a, b := plan.Segments[i], plan.Segments[j]
		if rank[a.Track] != rank[b.Track] {
			return rank[a.Track] < rank[b.Track]
		}
		if a.StartNS != b.StartNS {
			return a.StartNS < b.StartNS
		}
		return a.Digest < b.Digest
	})
}

// Digest returns a stable digest of the plan content, used as the merge
// stage output hash. It excludes volatile ordering metadata beyond what
// sortPlan already normalises.
func (p *Plan) Digest() (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", fault.New(fault.KindMerge, "cannot encode plan").At("merge").Wrap(err)
	}
	return hashHex(data), nil
}

// Write publishes the plan atomically into dir as plan.json. Nothing is
// written if encoding fails.
func (p *Plan) Write(dir string) (path string, digest string, err error) {
	if dir == "" {
		return "", "", fault.New(fault.KindArgument, "plan output directory required").At("merge")
	}
	data, encErr := json.MarshalIndent(p, "", "  ")
	if encErr != nil {
		return "", "", fault.New(fault.KindMerge, "cannot encode plan").At("merge").Wrap(encErr)
	}
	data = append(data, '\n')
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return "", "", fault.New(fault.KindInput, "cannot create merge directory").At("merge").Wrap(mkErr)
	}
	target := filepath.Join(dir, "plan.json")
	if err := writeFileAtomic(target, data); err != nil {
		return "", "", fault.New(fault.KindInput, "cannot publish plan").At("merge").Wrap(err)
	}
	return target, hashHex(data), nil
}

// RollbackRecord is one rolled-back artefact.
type RollbackRecord struct {
	Stage   string   `json:"stage"`
	Reason  string   `json:"reason"`
	Removed []string `json:"removed,omitempty"`
	Failed  []string `json:"failed,omitempty"`
}

// Rollback removes the given artefact paths beneath dir for the named
// stage, returning what was removed and what could not be. Missing files
// are not an error: rollback is idempotent. Paths escaping dir are
// rejected so a corrupt checkpoint cannot delete outside the workspace.
func Rollback(dir, stage, reason string, artefacts []string) (RollbackRecord, error) {
	rec := RollbackRecord{Stage: stage, Reason: reason}
	if dir == "" {
		return rec, fault.New(fault.KindArgument, "rollback requires a directory").At("rollback")
	}
	root, err := filepath.Abs(dir)
	if err != nil {
		return rec, fault.New(fault.KindInput, "cannot resolve workspace").At("rollback").Wrap(err)
	}
	for _, rel := range artefacts {
		target := rel
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, rel)
		}
		target, err = filepath.Abs(target)
		if err != nil {
			rec.Failed = append(rec.Failed, rel)
			continue
		}
		if target != root && !withinRoot(root, target) {
			return rec, fault.New(fault.KindMerge, "rollback path escapes workspace").
				At("rollback").WithTrack(stage)
		}
		if err := os.Remove(target); err != nil {
			if os.IsNotExist(err) {
				continue
			}
			rec.Failed = append(rec.Failed, rel)
			continue
		}
		rec.Removed = append(rec.Removed, rel)
	}
	if len(rec.Failed) > 0 {
		return rec, fault.New(fault.KindMerge, "rollback could not remove all artefacts").
			At("rollback").WithTrack(stage)
	}
	return rec, nil
}

func withinRoot(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return false
	}
	return rel != ".." && !hasParentEscape(rel)
}

func hasParentEscape(rel string) bool {
	return len(rel) >= 3 && rel[:3] == ".."+string(filepath.Separator)
}

// Invalidate applies an input change to the checkpoint plan and rolls the
// affected stages back, newest first. changed maps a stage name to the
// reason its input changed. Every stage downstream of a changed stage is
// invalidated too. It returns the rollback records in stage order.
func Invalidate(plan []string, changed map[string]string, rollback func(stage, reason string, downstream []string) (RollbackRecord, error)) ([]RollbackRecord, error) {
	if len(plan) == 0 {
		return nil, fault.New(fault.KindArgument, "invalidation requires a plan").At("rollback")
	}
	index := make(map[string]int, len(plan))
	for i, stage := range plan {
		index[stage] = i
	}
	for stage := range changed {
		if _, ok := index[stage]; !ok {
			return nil, fault.New(fault.KindArgument, "unknown stage "+stage).At("rollback")
		}
	}

	// Find the earliest changed stage; everything from there on is
	// affected, because stages form a dependency chain in plan order.
	earliest := len(plan)
	for stage := range changed {
		if index[stage] < earliest {
			earliest = index[stage]
		}
	}
	if earliest == len(plan) {
		return nil, nil
	}

	var records []RollbackRecord
	// Roll back from the end so downstream artefacts disappear before
	// the stage that produced their inputs.
	for i := len(plan) - 1; i >= earliest; i-- {
		stage := plan[i]
		reason, direct := changed[stage]
		if !direct {
			reason = "downstream of changed input"
		}
		rec, err := rollback(stage, reason, plan[i:])
		if err != nil {
			return records, err
		}
		records = append(records, rec)
	}
	// Return records in plan order for stable reporting.
	for i, j := 0, len(records)-1; i < j; i, j = i+1, j-1 {
		records[i], records[j] = records[j], records[i]
	}
	return records, nil
}
