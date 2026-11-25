package engine

import (
	"sort"

	"mediad/internal/dag"
	"mediad/internal/model"
	"mediad/internal/segment"
)

// buildManifest reconstructs descriptors from the cutter state cross-checked
// against the durable checkpoint ledger, then assembles the canonical
// manifest. Segment ordering is normalized inside NewManifest.
func buildManifest(
	base model.TimeBase,
	reps []segment.Representation,
	states []*repState,
	align *segment.Align,
	corruption []segment.CorruptionReport,
	cp *dag.Checkpoint,
) (*segment.Manifest, error) {
	var descs []*segment.Descriptor
	ledger := map[string]dag.SegmentCommit{}
	for _, c := range cp.Commits() {
		ledger[c.Key] = c
	}
	for _, r := range states {
		for _, d := range r.cutter.Segments() {
			// Trust the ledger for byte size: it reflects what was muxed,
			// while the descriptor is parser-derived.
			if commit, ok := ledger[dag.TaskKey(r.id, d.Index)]; ok {
				d.ByteSize = commit.ByteSize
				d.SHA256 = commit.SHA256
			}
			descs = append(descs, d)
		}
	}
	sort.Slice(descs, func(i, j int) bool {
		if descs[i].Index != descs[j].Index {
			return descs[i].Index < descs[j].Index
		}
		return descs[i].Representation < descs[j].Representation
	})
	m := segment.NewManifest(
		base.SegmentTicks, reps, descs, align.MaxDriftTicks(), corruption,
	)
	return m, nil
}
