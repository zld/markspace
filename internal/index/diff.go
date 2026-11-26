package index

import (
	"sort"

	"mediapipeline/pkg/media"
)

// SegmentDigest identifies one fragment by ordinal and content hash.
type SegmentDigest struct {
	Index int    `json:"index"`
	SHA   string `json:"sha256"`
}

// TrackDigest summarizes a track without storing sample bytes.
type TrackDigest struct {
	TrackID  uint32          `json:"track_id"`
	TrackSHA string          `json:"track_sha256"`
	Segments []SegmentDigest `json:"segments"`
}

// Snapshot is the comparable index state for one source path.
type Snapshot struct {
	Path      string        `json:"path"`
	SourceSHA string        `json:"source_sha256"`
	Tracks    []TrackDigest `json:"tracks"`
}

// Decision describes reuse versus recomputation.
type Decision struct {
	SourceUnchanged  bool             `json:"source_unchanged"`
	ReuseAll         bool             `json:"reuse_all"`
	ContainerOnly    bool             `json:"container_only"`
	ChangedTracks    []uint32         `json:"changed_tracks,omitempty"`
	UnchangedTracks  []uint32         `json:"unchanged_tracks,omitempty"`
	ChangedSegments  map[uint32][]int `json:"changed_segments,omitempty"`
	RecomputeSamples int              `json:"recompute_samples"`
	Scope            string           `json:"scope"`
	Reason           string           `json:"reason"`
}

// SnapshotFromInfo builds a comparable snapshot from parsed media metadata.
func SnapshotFromInfo(info *media.ContainerInfo) Snapshot {
	s := Snapshot{Path: info.Path, SourceSHA: info.SourceSHA256}
	for _, t := range info.Tracks {
		td := TrackDigest{TrackID: t.TrackID, TrackSHA: t.TrackSHA256}
		for _, seg := range t.Segments {
			td.Segments = append(td.Segments, SegmentDigest{Index: seg.Index, SHA: seg.SHA256})
		}
		s.Tracks = append(s.Tracks, td)
	}
	return s
}

// Diff compares old and new source/track/segment digests. A nil old snapshot
// means there is no reusable index state.
func Diff(old *Snapshot, next Snapshot) Decision {
	d := Decision{ChangedSegments: make(map[uint32][]int)}
	if old == nil {
		d.Scope = media.ScopeAll
		d.Reason = "no previous index"
		for _, t := range next.Tracks {
			d.ChangedTracks = append(d.ChangedTracks, t.TrackID)
			for _, seg := range t.Segments {
				d.ChangedSegments[t.TrackID] = append(d.ChangedSegments[t.TrackID], seg.Index)
				d.RecomputeSamples++
			}
		}
		return d
	}
	if old.SourceSHA == next.SourceSHA {
		d.SourceUnchanged = true
		d.ReuseAll = true
		d.Scope = media.ScopeNone
		d.Reason = "source sha256 unchanged"
		for _, t := range next.Tracks {
			d.UnchangedTracks = append(d.UnchangedTracks, t.TrackID)
		}
		return d
	}

	oldTracks := make(map[uint32]TrackDigest, len(old.Tracks))
	for _, t := range old.Tracks {
		oldTracks[t.TrackID] = t
	}
	nextTracks := make(map[uint32]TrackDigest, len(next.Tracks))
	for _, t := range next.Tracks {
		nextTracks[t.TrackID] = t
	}

	allTrackSHASame := true
	for _, nt := range next.Tracks {
		ot, ok := oldTracks[nt.TrackID]
		if !ok || ot.TrackSHA != nt.TrackSHA {
			allTrackSHASame = false
		}
		if ok && ot.TrackSHA == nt.TrackSHA {
			d.UnchangedTracks = append(d.UnchangedTracks, nt.TrackID)
			continue
		}

		d.ChangedTracks = append(d.ChangedTracks, nt.TrackID)
		oldSeg := make(map[int]string, len(ot.Segments))
		for _, seg := range ot.Segments {
			oldSeg[seg.Index] = seg.SHA
		}
		for _, seg := range nt.Segments {
			if oldSeg[seg.Index] != seg.SHA {
				d.ChangedSegments[nt.TrackID] = append(d.ChangedSegments[nt.TrackID], seg.Index)
				d.RecomputeSamples++
			}
		}
		sort.Ints(d.ChangedSegments[nt.TrackID])
	}
	for id := range oldTracks {
		if _, ok := nextTracks[id]; !ok {
			d.ChangedTracks = append(d.ChangedTracks, id)
			allTrackSHASame = false
		}
	}
	sort.Slice(d.ChangedTracks, func(i, j int) bool { return d.ChangedTracks[i] < d.ChangedTracks[j] })
	sort.Slice(d.UnchangedTracks, func(i, j int) bool { return d.UnchangedTracks[i] < d.UnchangedTracks[j] })

	if allTrackSHASame && len(old.Tracks) == len(next.Tracks) {
		d.ContainerOnly = true
		d.Scope = media.ScopeContainer
		d.Reason = "source changed but all track digests match"
		return d
	}
	if d.RecomputeSamples == 0 {
		d.Scope = media.ScopeTrack
		d.Reason = "track metadata changed but all segment digests match"
		return d
	}
	d.Scope = media.ScopeSegment
	d.Reason = "one or more segment digests changed"
	return d
}
