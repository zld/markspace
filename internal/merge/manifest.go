// Package merge combines completed task results into one deterministic
// manifest. Tasks are deduplicated by stable task key; identical inputs
// and rules always produce a byte-identical manifest.
package merge

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"

	"mediacore/internal/model"
)

// Item is one manifest entry: a single event's journey from source
// sample to output position.
type Item struct {
	TaskKey   string `json:"task_key"`
	SampleID  string `json:"sample_id"`
	SrcTrack  string `json:"src_track"`
	SrcOffset int64  `json:"src_offset"`
	OutTrack  string `json:"out_track"`
	OutOffset int64  `json:"out_offset"`
	Ticks     int64  `json:"ticks"`
	Bytes     uint32 `json:"bytes"`
	SHA256    string `json:"sha256"`
}

// TaskResult is one completed task's contribution to the manifest.
type TaskResult struct {
	TaskKey  string
	SampleID string
	Events   []model.Event // mapped and remuxed: OutTrackID/OutOffset set
}

// Manifest is the deterministic merge output.
type Manifest struct {
	Items  []Item `json:"items"`
	Digest string `json:"digest"` // sha256 of the canonical items encoding
}

// Build merges task results into a manifest. Duplicate task keys are
// dropped after their first occurrence in key-sorted order.
func Build(results []TaskResult) Manifest {
	sorted := make([]TaskResult, len(results))
	copy(sorted, results)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TaskKey < sorted[j].TaskKey })
	seen := map[string]bool{}
	var items []Item
	for _, r := range sorted {
		if seen[r.TaskKey] {
			continue
		}
		seen[r.TaskKey] = true
		for _, e := range r.Events {
			sum := sha256.Sum256(e.Payload)
			items = append(items, Item{
				TaskKey:   r.TaskKey,
				SampleID:  r.SampleID,
				SrcTrack:  e.TrackID,
				SrcOffset: e.SrcOffset,
				OutTrack:  e.OutTrackID,
				OutOffset: e.OutOffset,
				Ticks:     e.Ticks,
				Bytes:     e.Size,
				SHA256:    hex.EncodeToString(sum[:]),
			})
		}
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a.TaskKey != b.TaskKey {
			return a.TaskKey < b.TaskKey
		}
		if a.Ticks != b.Ticks {
			return a.Ticks < b.Ticks
		}
		if a.OutTrack != b.OutTrack {
			return a.OutTrack < b.OutTrack
		}
		return a.OutOffset < b.OutOffset
	})
	if items == nil {
		items = []Item{}
	}
	canonical, _ := json.Marshal(items)
	sum := sha256.Sum256(canonical)
	return Manifest{Items: items, Digest: hex.EncodeToString(sum[:])}
}

// Marshal renders the manifest as canonical JSON (fixed field order,
// no insignificant whitespace), suitable for byte-level comparison.
func (m Manifest) Marshal() []byte {
	b, _ := json.Marshal(m)
	return b
}
