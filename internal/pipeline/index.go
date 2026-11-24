// Package pipeline turns mapped tracks into the delivery artifacts: a
// losslessly remuxed container, a sparse keyframe index and a per-segment
// manifest with integrity hashes.
package pipeline

import (
	"sort"

	"mediad/internal/adapter"
	"mediad/internal/media"
)

// Keyframe is one random-access point on the output timeline.
type Keyframe struct {
	Seq  int   // event sequence within the track
	Time int64 // ns, output timeline
}

// TrackIndex is a sparse keyframe index for one mapped track.
type TrackIndex struct {
	OutputID int
	Keys     []Keyframe
	// Scanned is true when the container carried no keyframe table and the
	// index was recovered by scanning sample payloads.
	Scanned bool
	// Broken lists ranges that could not be indexed (damaged samples).
	Broken []media.Gap
}

// payloadKeyframe is the fixture/scan convention: bit 0 of the first
// payload byte marks a keyframe. Scanning is the fallback when the
// container's own random-access table (stss) is absent.
func payloadKeyframe(p []byte) bool { return len(p) > 0 && p[0]&1 == 1 }

// BuildIndex builds the sparse keyframe index for one mapped track from
// its events and their output timestamps. When the source track exposed no
// container keyframe table, keyframes are recovered by scanning payloads;
// damaged samples are recorded as broken, unrecoverable ranges.
func BuildIndex(mt adapter.MappedTrack, outTimes []int64) *TrackIndex {
	idx := &TrackIndex{OutputID: mt.OutputID}
	brokenStart := -1
	for i, ev := range mt.Track.Events {
		if i >= len(outTimes) {
			break
		}
		if ev.Damaged {
			if brokenStart < 0 {
				brokenStart = ev.Seq
			}
			continue
		}
		if brokenStart >= 0 {
			idx.Broken = append(idx.Broken, media.Gap{
				TrackID: mt.OutputID, FromSeq: brokenStart, ToSeq: ev.Seq,
				Reason: "damaged sample not indexable"})
			brokenStart = -1
		}
		key := ev.Keyframe
		if !mt.Track.HasKeyframeTable {
			key = payloadKeyframe(ev.Payload)
			idx.Scanned = true
		}
		if key {
			idx.Keys = append(idx.Keys, Keyframe{Seq: ev.Seq, Time: outTimes[i]})
		}
	}
	if brokenStart >= 0 && len(mt.Track.Events) > 0 {
		idx.Broken = append(idx.Broken, media.Gap{
			TrackID: mt.OutputID, FromSeq: brokenStart,
			ToSeq:  mt.Track.Events[len(mt.Track.Events)-1].Seq + 1,
			Reason: "damaged sample not indexable"})
	}
	sort.Slice(idx.Keys, func(i, j int) bool { return idx.Keys[i].Time < idx.Keys[j].Time })
	return idx
}

// Locate finds the latest keyframe at or before t. The boolean is false
// when the index has no keyframe early enough — callers then fall back to a
// scan or mark the segment unrecoverable.
func (idx *TrackIndex) Locate(t int64) (Keyframe, bool) {
	i := sort.Search(len(idx.Keys), func(i int) bool { return idx.Keys[i].Time > t })
	if i == 0 {
		return Keyframe{}, false
	}
	return idx.Keys[i-1], true
}
