package index

import (
	"sort"

	"medfix/internal/timeline"
)

// Query describes an index lookup. Zero fields mean "no constraint".
type Query struct {
	FromTick     int64
	ToTick       int64 // inclusive; 0 with FromTick 0 means whole index
	TrackID      string
	Kind         string
	KeyframeOnly bool
}

// Provenance is the reverse-lookup chain attached to every hit.
type Provenance struct {
	Fingerprint  string
	SourceID     string
	OffsetBegin  int64
	OffsetEnd    int64
	ContentHash  string
	RepairRuleID string
	IndexVersion int
}

// Hit is one query result with its full provenance chain.
type Hit struct {
	Entry
	Provenance Provenance
}

// NoKeyframeStatus is the explicit status returned for tracks without any
// sync sample, instead of silently returning nothing.
type NoKeyframeStatus struct {
	TrackID string
	Reason  string
}

// Query executes a lookup. Entries are sorted by ticks so range scans use
// binary search; keyframe-less tracks report an explicit status.
func (idx *Index) Query(q Query) ([]Hit, []NoKeyframeStatus, error) {
	if !sort.SliceIsSorted(idx.Entries, func(i, j int) bool {
		return entryLess(idx.Entries[i], idx.Entries[j])
	}) {
		return nil, nil, timeline.NewError(timeline.CodeValidation, "query",
			"", -1, "index entries not sorted", nil)
	}
	statuses := idx.keyframeStatus(q)

	lo := 0
	if q.FromTick > 0 {
		lo = sort.Search(len(idx.Entries), func(i int) bool {
			return idx.Entries[i].Ticks >= q.FromTick
		})
	}
	var hits []Hit
	for i := lo; i < len(idx.Entries); i++ {
		e := idx.Entries[i]
		if q.ToTick > 0 && e.Ticks > q.ToTick {
			break
		}
		if q.TrackID != "" && e.TrackID != q.TrackID {
			continue
		}
		if q.Kind != "" && e.Kind != q.Kind {
			continue
		}
		if q.KeyframeOnly && !e.Keyframe {
			continue
		}
		hits = append(hits, Hit{
			Entry: e,
			Provenance: Provenance{
				Fingerprint:  e.Fingerprint,
				SourceID:     e.SourceID,
				OffsetBegin:  e.OffsetBegin,
				OffsetEnd:    e.OffsetEnd,
				ContentHash:  e.ContentHash,
				RepairRuleID: e.RuleID,
				IndexVersion: idx.Version,
			},
		})
	}
	return hits, statuses, nil
}

// NearestKeyframe returns the latest keyframe at or before tick on the
// given track — the random-access entry point for seeking.
func (idx *Index) NearestKeyframe(trackID string, tick int64) (Hit, bool, error) {
	n := len(idx.Entries)
	pos := sort.Search(n, func(i int) bool {
		return idx.Entries[i].Ticks > tick
	})
	for i := pos - 1; i >= 0; i-- {
		e := idx.Entries[i]
		if e.TrackID == trackID && e.Keyframe {
			return Hit{Entry: e, Provenance: idx.provenance(e)}, true, nil
		}
	}
	return Hit{}, false, nil
}

// Keyframes returns all sync samples of a track in tick order.
func (idx *Index) Keyframes(trackID string) ([]Hit, NoKeyframeStatus, error) {
	var hits []Hit
	found := false
	for _, e := range idx.Entries {
		if e.TrackID != trackID {
			continue
		}
		if e.Keyframe {
			found = true
			hits = append(hits, Hit{Entry: e, Provenance: idx.provenance(e)})
		}
	}
	if !found {
		return nil, NoKeyframeStatus{
			TrackID: trackID,
			Reason:  "track contains no sync sample (stss absent or empty)",
		}, nil
	}
	return hits, NoKeyframeStatus{}, nil
}

// MetadataEvents returns non-sample entries (subtitle/chapter/id3/emsg/sei).
func (idx *Index) MetadataEvents() []Hit {
	var hits []Hit
	for _, e := range idx.Entries {
		if e.Kind == string(timeline.KindSample) {
			continue
		}
		hits = append(hits, Hit{Entry: e, Provenance: idx.provenance(e)})
	}
	return hits
}

// KeyframeHitRate returns keyframe samples divided by all sample entries —
// the acceptance metric reported by the demo command.
func (idx *Index) KeyframeHitRate() float64 {
	var samples, keys int
	for _, e := range idx.Entries {
		if e.Kind != string(timeline.KindSample) {
			continue
		}
		samples++
		if e.Keyframe {
			keys++
		}
	}
	if samples == 0 {
		return 0
	}
	return float64(keys) / float64(samples)
}

func (idx *Index) provenance(e Entry) Provenance {
	return Provenance{
		Fingerprint: e.Fingerprint, SourceID: e.SourceID,
		OffsetBegin: e.OffsetBegin, OffsetEnd: e.OffsetEnd,
		ContentHash: e.ContentHash, RepairRuleID: e.RuleID,
		IndexVersion: idx.Version,
	}
}

func (idx *Index) keyframeStatus(q Query) []NoKeyframeStatus {
	var out []NoKeyframeStatus
	for _, id := range idx.NoKeyframeTracks {
		if q.TrackID != "" && id != q.TrackID {
			continue
		}
		out = append(out, NoKeyframeStatus{
			TrackID: id, Reason: "no sync samples in this track",
		})
	}
	// Also cover sample-bearing tracks that happen to have no keyframe.
	hasKey := map[string]bool{}
	hasAny := map[string]bool{}
	for _, e := range idx.Entries {
		if q.TrackID != "" && e.TrackID != q.TrackID {
			continue
		}
		if e.Kind != string(timeline.KindSample) {
			continue
		}
		hasAny[e.TrackID] = true
		if e.Keyframe {
			hasKey[e.TrackID] = true
		}
	}
	for id := range hasAny {
		if !hasKey[id] {
			out = append(out, NoKeyframeStatus{
				TrackID: id, Reason: "sample track without sync samples",
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].TrackID < out[j].TrackID })
	return out
}
