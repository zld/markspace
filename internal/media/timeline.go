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

// expandStts turns run-length (count, delta) entries into one delta per
// sample and accumulates the DTS. It rejects zero durations and counts
// that overflow the declared sample count.
func expandStts(tb *TrackBoxes, n uint32) ([]int64, []uint32, error) {
	dts := make([]int64, n)
	dur := make([]uint32, n)
	var idx uint32
	var t int64
	for _, e := range tb.Stts.Entries {
		if e.SampleDelta == 0 {
			return nil, nil, clock.NewError(clock.KindTimeline, "stts",
				"zero sample duration").WithTrack(tb.TrackID)
		}
		for k := uint32(0); k < e.SampleCount; k++ {
			if idx >= n {
				return nil, nil, clock.NewError(clock.KindTimeline, "stts",
					"entry counts exceed stsz sample count").WithTrack(tb.TrackID)
			}
			dts[idx] = t
			dur[idx] = e.SampleDelta
			t += int64(e.SampleDelta)
			idx++
		}
	}
	if idx != n {
		return nil, nil, clock.NewError(clock.KindTimeline, "stts",
			fmt.Sprintf("stts covers %d samples, stsz declares %d", idx, n)).WithTrack(tb.TrackID)
	}
	return dts, dur, nil
}

// expandCtts produces one composition offset per sample; v1 is signed.
func expandCtts(tb *TrackBoxes, n uint32) ([]int32, error) {
	off := make([]int32, n)
	if tb.Ctts == nil {
		return off, nil
	}
	var idx uint32
	for _, e := range tb.Ctts.Entries {
		v := int32(e.SampleOffsetV1)
		if tb.Ctts.GetVersion() == 0 {
			v = int32(e.SampleOffsetV0)
		}
		for k := uint32(0); k < e.SampleCount; k++ {
			if idx >= n {
				return nil, clock.NewError(clock.KindTimeline, "ctts",
					"entry counts exceed sample count").WithTrack(tb.TrackID)
			}
			off[idx] = v
			idx++
		}
	}
	if idx != n {
		return nil, clock.NewError(clock.KindTimeline, "ctts",
			"ctts does not cover every sample").WithTrack(tb.TrackID)
	}
	return off, nil
}

// chunkOffsets returns one absolute offset per chunk, accepting stco or co64.
func chunkOffsets(tb *TrackBoxes) ([]int64, error) {
	switch {
	case tb.Co64 != nil:
		out := make([]int64, len(tb.Co64.ChunkOffset))
		for i, v := range tb.Co64.ChunkOffset {
			out[i] = int64(v)
		}
		return out, nil
	case tb.Stco != nil:
		out := make([]int64, len(tb.Stco.ChunkOffset))
		for i, v := range tb.Stco.ChunkOffset {
			out[i] = int64(v)
		}
		return out, nil
	default:
		return nil, clock.NewError(clock.KindTimeline, "stco",
			"no chunk offset table").WithTrack(tb.TrackID)
	}
}

// samplesPerChunk expands the stsc run-length table into one value/chunk.
func samplesPerChunk(tb *TrackBoxes, chunkCount uint32) ([]uint32, error) {
	out := make([]uint32, chunkCount)
	entries := tb.Stsc.Entries
	for c := uint32(0); c < chunkCount; c++ {
		// stsc.FirstChunk is 1-based; pick the last entry still <= c+1.
		chosen := -1
		for i := range entries {
			if entries[i].FirstChunk <= c+1 {
				chosen = i
			}
		}
		if chosen < 0 {
			return nil, clock.NewError(clock.KindTimeline, "stsc",
				fmt.Sprintf("no stsc entry covers chunk %d", c+1)).WithTrack(tb.TrackID)
		}
		out[c] = entries[chosen].SamplesPerChunk
	}
	return out, nil
}

// detectAnomalies records every region that violates the expected
// monotonic, fixed-stride timeline. The nominal stride is the track's
// regular frame cadence; one interval is emitted per discontinuity and
// classified as regression, overlap or gap. Atypical stts durations (a
// dropped/duplicated frame) are folded into the same region rather than
// reported twice.
func detectAnomalies(t *TrackTimeline, pts []int64, dur []uint32) {
	nominal := nominalStride(dur)
	reported := map[uint32]bool{}
	add := func(kind AnomalyKind, i uint32, start, end int64, detail string) {
		if reported[i] {
			return
		}
		reported[i] = true
		t.Anomalies = append(t.Anomalies, AnomalyInterval{
			Kind: kind, TrackID: t.TrackID,
			StartTick: start, EndTick: end,
			StartIndex: i, EndIndex: i, Detail: detail,
		})
	}
	// Compare every presentation step against the nominal stride. This
	// catches regressions, overlaps and gaps regardless of how the stts
	// run-length table encoded the surrounding durations.
	for i := 1; i < len(pts); i++ {
		actual := pts[i] - pts[i-1]
		idx := uint32(i)
		switch {
		case actual < 0:
			add(AnomalyRegression, idx, pts[i-1], pts[i],
				fmt.Sprintf("pts %d -> %d", pts[i-1], pts[i]))
		case actual < nominal:
			add(AnomalyOverlap, idx, pts[i], pts[i-1]+nominal,
				fmt.Sprintf("step %d < nominal %d", actual, nominal))
		case actual > nominal:
			add(AnomalyGap, idx, pts[i-1]+nominal, pts[i],
				fmt.Sprintf("gap %d ticks before pts %d", actual-nominal, pts[i]))
		}
	}
	// Surface any remaining atypical duration that did not produce a PTS
	// step anomaly, so a stts-only discontinuity is still untrusted.
	for i := range dur {
		if int64(dur[i]) != nominal {
			add(AnomalyGap, uint32(i), pts[i], pts[i]+int64(dur[i]),
				fmt.Sprintf("duration %d != nominal stride %d", dur[i], nominal))
		}
	}
	mergeAdjacent(t)
}

// mergeAdjacent folds same-kind intervals that touch or overlap into one
// region, so one physical discontinuity is reported exactly once.
func mergeAdjacent(t *TrackTimeline) {
	if len(t.Anomalies) < 2 {
		return
	}
	merged := t.Anomalies[:1]
	for _, a := range t.Anomalies[1:] {
		last := &merged[len(merged)-1]
		if a.Kind == last.Kind && a.StartTick <= last.EndTick {
			if a.EndTick > last.EndTick {
				last.EndTick = a.EndTick
			}
			if a.EndIndex > last.EndIndex {
				last.EndIndex = a.EndIndex
			}
			continue
		}
		merged = append(merged, a)
	}
	t.Anomalies = merged
}

// nominalStride returns the most common sample delta, i.e. the track's
// regular frame cadence rather than an average frame rate.
func nominalStride(dur []uint32) int64 {
	counts := map[uint32]int{}
	var best uint32
	for _, d := range dur {
		counts[d]++
		if counts[d] > counts[best] {
			best = d
		}
	}
	return int64(best)
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
