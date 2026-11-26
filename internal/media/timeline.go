package media

import (
	"fmt"

	"replay/internal/clock"
)

// AnomalyKind gives a stable machine-readable reason per abnormal region.
type AnomalyKind string

const (
	AnomalyRegression AnomalyKind = "time_regression"
	AnomalyOverlap    AnomalyKind = "overlap"
	AnomalyGap        AnomalyKind = "gap"
)

// AnomalyInterval describes one PTS region that violates monotonic,
// gapless playback. Bounds are native track ticks.
type AnomalyInterval struct {
	Kind       AnomalyKind `json:"kind"`
	TrackID    uint32      `json:"track_id"`
	StartTick  int64       `json:"start_tick"`
	EndTick    int64       `json:"end_tick"`
	StartIndex uint32      `json:"start_sample"`
	EndIndex   uint32      `json:"end_sample"`
	Detail     string      `json:"detail,omitempty"`
}

// Sample is one timeline entry in the track's native time base.
type Sample struct {
	Index   uint32 // 0-based
	Chunk   uint32 // 0-based chunk
	DTS     int64  // decoding time in track ticks
	PTS     int64  // presentation time in track ticks
	CTSOff  int32  // composition offset
	Dur     uint32 // stts delta in track ticks
	Size    uint32 // payload bytes
	Offset  int64  // absolute payload byte offset in the source file
	IsSync  bool
}

// TrackTimeline is the fully expanded, indexable sample timeline.
type TrackTimeline struct {
	TrackID   uint32
	Handler   HandlerType
	Timescale uint32
	Samples   []Sample
	SyncIdx   []uint32 // sample indices flagged sync
	Anomalies []AnomalyInterval
	Duration  int64 // DTS of last sample end, track ticks
}

// Timeline groups the per-track timelines of one parsed container.
type Timeline struct {
	Tracks []*TrackTimeline
	Video  *TrackTimeline
	Audio  *TrackTimeline
}

// BuildTrack validates the tables and expands them into a full timeline.
func BuildTrack(tb *TrackBoxes) (*TrackTimeline, error) {
	if tb.Timescale == 0 {
		return nil, clock.NewError(clock.KindTimeline, "mdhd", "zero timescale").WithTrack(tb.TrackID)
	}
	if tb.Stts == nil || tb.Stsc == nil || tb.Stsz == nil {
		return nil, clock.NewError(clock.KindTimeline, "stbl",
			"missing required stts/stsc/stsz table").WithTrack(tb.TrackID)
	}
	n := tb.Stsz.SampleCount
	if n == 0 {
		return nil, clock.NewError(clock.KindTimeline, "stsz", "no samples").WithTrack(tb.TrackID)
	}
	sizes := make([]uint32, n)
	if tb.Stsz.SampleSize != 0 {
		for i := range sizes {
			sizes[i] = tb.Stsz.SampleSize
		}
	} else if uint32(len(tb.Stsz.EntrySize)) != n {
		return nil, clock.NewError(clock.KindTimeline, "stsz",
			fmt.Sprintf("entry size count %d != sample count %d", len(tb.Stsz.EntrySize), n)).
			WithTrack(tb.TrackID)
	} else {
		copy(sizes, tb.Stsz.EntrySize)
	}
	dts, dur, err := expandStts(tb, n)
	if err != nil {
		return nil, err
	}
	ctsoff, err := expandCtts(tb, n)
	if err != nil {
		return nil, err
	}
	chunks, err := chunkOffsets(tb)
	if err != nil {
		return nil, err
	}
	perChunk, err := samplesPerChunk(tb, uint32(len(chunks)))
	if err != nil {
		return nil, err
	}
	t := &TrackTimeline{
		TrackID: tb.TrackID, Handler: tb.Handler, Timescale: tb.Timescale,
		Samples: make([]Sample, 0, n),
	}
	pts := make([]int64, n)
	sync := map[uint32]bool{}
	if tb.Stss != nil {
		for _, s := range tb.Stss.SampleNumber {
			if s == 0 || s > n {
				return nil, clock.NewError(clock.KindTimeline, "stss",
					fmt.Sprintf("sync sample %d out of range", s)).WithTrack(tb.TrackID)
			}
			sync[s-1] = true
		}
	}
	var sampleIdx, total uint32
	for c, coff := range chunks {
		cur := coff
		for k := uint32(0); k < perChunk[c]; k++ {
			if sampleIdx >= n {
				return nil, clock.NewError(clock.KindTimeline, "stsc",
					"chunk layout produces more samples than declared").WithTrack(tb.TrackID)
			}
			pts[sampleIdx] = dts[sampleIdx] + int64(ctsoff[sampleIdx])
			isSync := tb.Handler == HandlerAudio || tb.Stss == nil || sync[sampleIdx]
			t.Samples = append(t.Samples, Sample{
				Index: sampleIdx, Chunk: uint32(c), DTS: dts[sampleIdx],
				PTS: pts[sampleIdx], CTSOff: ctsoff[sampleIdx], Dur: dur[sampleIdx],
				Size: sizes[sampleIdx], Offset: cur, IsSync: isSync,
			})
			if isSync {
				t.SyncIdx = append(t.SyncIdx, sampleIdx)
			}
			cur += int64(sizes[sampleIdx])
			sampleIdx++
			total++
		}
	}
	if total != n {
		return nil, clock.NewError(clock.KindTimeline, "stsc",
			fmt.Sprintf("chunk layout covers %d of %d samples", total, n)).WithTrack(tb.TrackID)
	}
	t.Duration = dts[n-1] + int64(dur[n-1])
	detectAnomalies(t, pts, dur)
	return t, nil
}

// Build builds every track timeline and selects primary video/audio.
func Build(fi *FileInfo) (*Timeline, error) {
	tl := &Timeline{}
	for _, tb := range fi.Tracks {
		tr, err := BuildTrack(tb)
		if err != nil {
			return nil, err
		}
		tl.Tracks = append(tl.Tracks, tr)
		switch tr.Handler {
		case HandlerVideo:
			if tl.Video == nil {
				tl.Video = tr
			}
		case HandlerAudio:
			if tl.Audio == nil {
				tl.Audio = tr
			}
		}
	}
	if tl.Video == nil {
		return nil, clock.NewError(clock.KindTimeline, "tracks", "no video track")
	}
	return tl, nil
}

// SyncAtOrBefore returns the index of the last sync sample with PTS <= tick,
// or -1 when no keyframe can back the requested start.
func (t *TrackTimeline) SyncAtOrBefore(tick int64) int {
	idx := -1
	for _, si := range t.SyncIdx {
		if t.Samples[si].PTS <= tick {
			idx = int(si)
		} else {
			break
		}
	}
	return idx
}

// FirstSampleAtOrAfter is the first sample whose PTS >= tick.
func (t *TrackTimeline) FirstSampleAtOrAfter(tick int64) int {
	lo, hi := 0, len(t.Samples)
	for lo < hi {
		mid := (lo + hi) / 2
		if t.Samples[mid].PTS < tick {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo
}

// AffectedByAnomaly reports whether [startPTS, endPTS) touches an anomaly.
func (t *TrackTimeline) AffectedByAnomaly(startPTS, endPTS int64) *AnomalyInterval {
	for i := range t.Anomalies {
		a := &t.Anomalies[i]
		if a.StartTick < endPTS && startPTS < a.EndTick {
			return a
		}
	}
	return nil
}
