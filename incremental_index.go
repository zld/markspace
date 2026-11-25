package mediakernel

import "sort"

// Change classification for one source between two index generations.
type ChangeKind string

const (
	ChangeUnknown       ChangeKind = "unknown"
	ChangeNone          ChangeKind = "unchanged"
	ChangeContainerOnly ChangeKind = "container-only"
	ChangeContent       ChangeKind = "content"
	ChangeNew           ChangeKind = "new-source"
)

// SourceState is one indexed source with its independent content and container
// digests. JSON field names are part of the canonical index digest.
type SourceState struct {
	SourceID        string `json:"source_id"`
	ContentDigest   Digest `json:"content_digest"`
	ContainerDigest Digest `json:"container_digest"`
}

// AssetDependency declares which sources an asset is derived from and whether
// the derivation reads encapsulation (offsets, packet framing) or only decoded
// content (timeline, samples, subtitle text).
type AssetDependency struct {
	AssetID        string   `json:"asset_id"`
	Kind           string   `json:"kind"` // dialogue | subtitle | sfx
	Sources        []string `json:"sources"`
	NeedsContainer bool     `json:"needs_container"`
}

// AssetDecision records what the incremental plan will do with one asset.
type AssetDecision struct {
	AssetID string `json:"asset_id"`
	Kind    string `json:"kind"`
	Action  string `json:"action"` // recompute | reuse
	Reason  string `json:"reason"`
}

// Plan is the per-run incremental result.
type Plan struct {
	Recompute      []AssetDecision `json:"recompute"`
	Reuse          []AssetDecision `json:"reuse"`
	ChangedSources []SourceChange  `json:"changed_sources"`
	RecomputeCount int             `json:"recompute_count"`
	ReuseCount     int             `json:"reuse_count"`
}

// SourceChange explains how a source changed.
type SourceChange struct {
	SourceID string     `json:"source_id"`
	Change   ChangeKind `json:"change"`
}

// IncrementalIndex keeps the previous generation of source states and decides
// which assets must be recomputed. It is safe for single-goroutine planning.
type IncrementalIndex struct {
	prev map[string]SourceState
}

// NewIncrementalIndex builds an index from a prior generation (nil/empty means
// a first run where every asset is recomputed).
func NewIncrementalIndex(prev []SourceState) *IncrementalIndex {
	idx := &IncrementalIndex{prev: make(map[string]SourceState, len(prev))}
	for _, s := range prev {
		idx.prev[s.SourceID] = s
	}
	return idx
}

// Classify compares one new source state against the stored generation.
func (i *IncrementalIndex) Classify(cur SourceState) ChangeKind {
	old, ok := i.prev[cur.SourceID]
	if !ok {
		return ChangeNew
	}
	contentSame := old.ContentDigest.Equal(cur.ContentDigest)
	containerSame := old.ContainerDigest.Equal(cur.ContainerDigest)
	switch {
	case contentSame && containerSame:
		return ChangeNone
	case contentSame:
		return ChangeContainerOnly
	default:
		return ChangeContent
	}
}

// Plan computes recompute/reuse decisions for all declared assets given the
// current source states. An asset is recomputed when any of:
//
//   - a source it depends on is new or has changed content;
//   - a source it depends on changed its container and the asset needs
//     encapsulation data;
//   - the asset was never built before (implicit on a first run).
//
// Everything else is reused. Output slices are sorted by asset/source ID so
// the affected-asset set is deterministic across repeated runs.
func (i *IncrementalIndex) Plan(current []SourceState, assets []AssetDependency) (Plan, error) {
	changes := make(map[string]ChangeKind, len(current))
	changeList := make([]SourceChange, 0, len(current))
	for _, cur := range current {
		kind := i.Classify(cur)
		changes[cur.SourceID] = kind
		if kind != ChangeNone {
			changeList = append(changeList, SourceChange{SourceID: cur.SourceID, Change: kind})
		}
	}
	sort.Slice(changeList, func(a, b int) bool { return changeList[a].SourceID < changeList[b].SourceID })

	plan := Plan{ChangedSources: changeList}
	for _, asset := range assets {
		decision := AssetDecision{AssetID: asset.AssetID, Kind: asset.Kind, Action: "reuse"}
		if len(i.prev) == 0 {
			decision.Action = "recompute"
			decision.Reason = "first run: no prior index"
			plan.Recompute = append(plan.Recompute, decision)
			continue
		}
		if reason, must := i.assetMustRecompute(asset, changes); must {
			decision.Action = "recompute"
			decision.Reason = reason
			plan.Recompute = append(plan.Recompute, decision)
		} else {
			decision.Reason = "all dependencies unchanged"
			plan.Reuse = append(plan.Reuse, decision)
		}
	}
	sortDecisions(plan.Recompute)
	sortDecisions(plan.Reuse)
	plan.RecomputeCount = len(plan.Recompute)
	plan.ReuseCount = len(plan.Reuse)
	return plan, nil
}

func (i *IncrementalIndex) assetMustRecompute(asset AssetDependency, changes map[string]ChangeKind) (string, bool) {
	for _, src := range asset.Sources {
		kind, ok := changes[src]
		if !ok {
			return "dependency source missing from current index: " + src, true
		}
		switch kind {
		case ChangeNew:
			return "new source " + src, true
		case ChangeContent:
			return "content change in source " + src, true
		case ChangeContainerOnly:
			if asset.NeedsContainer {
				return "container change in source " + src, true
			}
		}
	}
	return "", false
}

func sortDecisions(d []AssetDecision) {
	sort.Slice(d, func(a, b int) bool { return d[a].AssetID < d[b].AssetID })
}

// IndexDigest hashes the canonical JSON of a source-state generation. The
// states are sorted first so the digest depends only on content, not order.
func IndexDigest(states []SourceState) (Digest, error) {
	sorted := append([]SourceState(nil), states...)
	sort.Slice(sorted, func(a, b int) bool { return sorted[a].SourceID < sorted[b].SourceID })
	return DigestCanonical(sorted)
}

// Snapshot returns the current source states as a sorted slice.
func (i *IncrementalIndex) Snapshot() []SourceState {
	out := make([]SourceState, 0, len(i.prev))
	for _, s := range i.prev {
		out = append(out, s)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].SourceID < out[b].SourceID })
	return out
}
