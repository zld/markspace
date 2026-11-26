// Package index builds random-access and half-open sample-range indexes.
package index

import (
	"sort"

	"replayctl/internal/fault"
	mp4scan "replayctl/internal/mp4"
)

// Index contains per-media random-access indexes.
type Index struct {
	Movie *mp4scan.Movie
	Video *TrackIndex
	Audio *TrackIndex
}

// TrackIndex indexes one MP4 track in that track's timescale.
type TrackIndex struct {
	Track       *mp4scan.Track
	syncIndexes []int
}

// Build validates monotonic sample timing and constructs video/audio indexes.
func Build(movie *mp4scan.Movie) (*Index, error) {
	if movie == nil {
		return nil, fault.New(fault.Index, fault.StageIndex, "", -1, -1, "nil movie")
	}
	idx := &Index{Movie: movie}
	if tr := movie.Track("video"); tr != nil {
		ti, err := newTrackIndex(movie.Path, tr)
		if err != nil {
			return nil, err
		}
		idx.Video = ti
	}
	if tr := movie.Track("audio"); tr != nil {
		ti, err := newTrackIndex(movie.Path, tr)
		if err != nil {
			return nil, err
		}
		idx.Audio = ti
	}
	if idx.Video == nil && idx.Audio == nil {
		return nil, fault.New(fault.Unsupported, fault.StageIndex, movie.Path, -1, -1, "no indexed media tracks")
	}
	return idx, nil
}

func newTrackIndex(path string, tr *mp4scan.Track) (*TrackIndex, error) {
	ti := &TrackIndex{Track: tr}
	var lastDTS int64
	for i, smp := range tr.Samples {
		if i > 0 && smp.DTS < lastDTS {
			return nil, fault.New(fault.InvalidBox, fault.StageIndex, path, smp.Offset, smp.DTS, "non-monotonic DTS in track %d", tr.ID)
		}
		lastDTS = smp.DTS
		if smp.Sync {
			ti.syncIndexes = append(ti.syncIndexes, i)
		}
	}
	return ti, nil
}

// RandomAccessBefore returns the latest sync sample with DTS <= startTick.
// If no sync point is at or before start, the first sync sample is returned
// only when it is itself at start; otherwise found=false.
func (ti *TrackIndex) RandomAccessBefore(startTick int64) (mp4scan.Sample, bool) {
	if ti == nil || len(ti.Track.Samples) == 0 || len(ti.syncIndexes) == 0 {
		return mp4scan.Sample{}, false
	}
	pos := sort.Search(len(ti.syncIndexes), func(i int) bool {
		return ti.Track.Samples[ti.syncIndexes[i]].DTS > startTick
	}) - 1
	if pos < 0 {
		return mp4scan.Sample{}, false
	}
	return ti.Track.Samples[ti.syncIndexes[pos]], true
}

// SamplesIn returns samples whose DTS lies in the half-open [start,end) range.
func (ti *TrackIndex) SamplesIn(startTick, endTick int64) []mp4scan.Sample {
	if ti == nil || startTick >= endTick {
		return nil
	}
	s := ti.Track.Samples
	first := sort.Search(len(s), func(i int) bool { return s[i].DTS >= startTick })
	last := sort.Search(len(s), func(i int) bool { return s[i].DTS >= endTick })
	if first >= last {
		return nil
	}
	return s[first:last]
}

// Slice returns the random-access preroll point and exactly the [start,end) samples.
func (ti *TrackIndex) Slice(startTick, endTick int64) (rap *mp4scan.Sample, samples []mp4scan.Sample) {
	if smp, ok := ti.RandomAccessBefore(startTick); ok {
		rap = &smp
	}
	return rap, ti.SamplesIn(startTick, endTick)
}

// Len returns the number of indexed samples.
func (ti *TrackIndex) Len() int {
	if ti == nil {
		return 0
	}
	return len(ti.Track.Samples)
}
