// Package repair turns raw demuxed blocks into a monotonic, track-aligned
// stream. This file picks the reference track and measures audio/video skew.
package repair

import (
	"sort"

	"mediad/internal/ebml"
	"mediad/internal/media"
)

// chooseReference prefers the requested track, then track 1, then the lowest
// video track, then the lowest of any type.
func chooseReference(tracks []ebml.TrackInfo, want media.TrackID) media.TrackID {
	for _, t := range tracks {
		if media.TrackID(t.Number) == want {
			return want
		}
	}
	sorted := append([]ebml.TrackInfo(nil), tracks...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Number < sorted[j].Number })
	for _, t := range sorted {
		if t.Type == 1 {
			return media.TrackID(t.Number)
		}
	}
	if len(sorted) > 0 {
		return media.TrackID(sorted[0].Number)
	}
	return want
}

// audioVideoDeltas pairs each video block with the nearest audio block and
// records the absolute gap. It is used for the sync-error report.
func audioVideoDeltas(blocks []media.Block) []int64 {
	var audio []int64
	for _, b := range blocks {
		if b.TrackType == media.TrackAudio {
			audio = append(audio, b.TimeNs)
		}
	}
	if len(audio) == 0 {
		return nil
	}
	sort.Slice(audio, func(i, j int) bool { return audio[i] < audio[j] })
	var deltas []int64
	j := 0
	for _, b := range blocks {
		if b.TrackType != media.TrackVideo {
			continue
		}
		for j+1 < len(audio) && abs64(audio[j+1]-b.TimeNs) <= abs64(audio[j]-b.TimeNs) {
			j++
		}
		deltas = append(deltas, abs64(audio[j]-b.TimeNs))
	}
	return deltas
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

// MaxAVSkewNs returns the worst audio/video gap observed.
func (r *Result) MaxAVSkewNs() int64 {
	var max int64
	for _, d := range r.AVDeltasNs {
		if d > max {
			max = d
		}
	}
	return max
}
