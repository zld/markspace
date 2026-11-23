package probe

import (
	"fmt"

	"github.com/abema/go-mp4"
	"github.com/broadcast/mediad/internal/model"
)

// expandStts materializes per-sample decode deltas and detects backward,
// duplicate and gap anomalies directly on the rational timeline.
func expandStts(stts *mp4.Stts, sampleCount int, errs *model.ErrorSet, t *model.Track) []model.Rat {
	deltas := make([]model.Rat, 0, sampleCount)
	ts := uint32(t.Timescale)
	for _, e := range stts.Entries {
		if e.SampleDelta == 0 {
			errs.Add(model.MediaError{
				Kind: model.KindTimestampDuplicate, TrackID: t.ID,
				SampleIndex: len(deltas), Severity: model.SeverityWarning,
				Detail: fmt.Sprintf("stts run of %d zero-delta samples", e.SampleCount),
			})
		}
		d, err := model.FromUnits(int64(e.SampleDelta), ts)
		if err != nil {
			errs.Add(model.MediaError{Kind: model.KindIntegerOverflow, TrackID: t.ID, SampleIndex: len(deltas), Detail: err.Error()})
			return deltas
		}
		for i := uint32(0); i < e.SampleCount && len(deltas) < sampleCount; i++ {
			deltas = append(deltas, d)
		}
	}
	for len(deltas) < sampleCount {
		errs.Add(model.MediaError{
			Kind: model.KindTruncatedBox, TrackID: t.ID, SampleIndex: len(deltas),
			Detail: "stts covers fewer samples than stsz",
		})
		d, _ := model.FromUnits(0, ts)
		deltas = append(deltas, d)
	}
	return deltas
}

// expandCtts returns per-sample composition offsets (zero when absent).
func expandCtts(ctts *mp4.Ctts, sampleCount int, errs *model.ErrorSet, t *model.Track) []model.Rat {
	zero, err := model.NewRat(0, 1)
	if err != nil {
		panic(err)
	}
	out := make([]model.Rat, sampleCount)
	for i := range out {
		out[i] = zero
	}
	if ctts == nil {
		return out
	}
	idx := 0
	for ei, e := range ctts.Entries {
		off, err := model.FromUnits(ctts.GetSampleOffset(ei), t.Timescale)
		if err != nil {
			errs.Add(model.MediaError{Kind: model.KindIntegerOverflow, TrackID: t.ID, SampleIndex: idx, Detail: err.Error()})
			continue
		}
		for i := uint32(0); i < e.SampleCount && idx < sampleCount; i++ {
			out[idx] = off
			idx++
		}
	}
	return out
}

// expandChunks walks stsc + stco/co64 to give every sample its absolute
// mdat offset and owning chunk index.
func expandChunks(stsc *mp4.Stsc, stco *mp4.Stco, co64 *mp4.Co64, sizes []uint32,
	errs *model.ErrorSet, t *model.Track) ([]uint64, []int) {

	chunkOffsets := make([]uint64, 0)
	switch {
	case co64 != nil:
		chunkOffsets = append(chunkOffsets, co64.ChunkOffset...)
	case stco != nil:
		for _, o := range stco.ChunkOffset {
			chunkOffsets = append(chunkOffsets, uint64(o))
		}
	default:
		errs.Add(model.MediaError{
			Kind: model.KindTruncatedBox, TrackID: t.ID, SampleIndex: -1,
			Detail: "no stco/co64 chunk offset table",
		})
	}

	// samplesPerChunk[i] applies to chunk index i (0-based).
	samplesPerChunk := make([]uint32, len(chunkOffsets))
	for ci := range samplesPerChunk {
		samplesPerChunk[ci] = 1
	}
	for ei, e := range stsc.Entries {
		first := int(e.FirstChunk) - 1 // stsc first_chunk is 1-based
		last := len(chunkOffsets) - 1
		if ei+1 < len(stsc.Entries) {
			last = int(stsc.Entries[ei+1].FirstChunk) - 2
		}
		if first < 0 || first >= len(samplesPerChunk) {
			errs.Add(model.MediaError{
				Kind: model.KindSampleOutOfBounds, TrackID: t.ID, SampleIndex: -1,
				Detail: fmt.Sprintf("stsc first_chunk %d out of range", e.FirstChunk),
			})
			continue
		}
		if last >= len(samplesPerChunk) {
			last = len(samplesPerChunk) - 1
		}
		for ci := first; ci <= last; ci++ {
			samplesPerChunk[ci] = e.SamplesPerChunk
		}
	}

	offsets := make([]uint64, len(sizes))
	chunkOf := make([]int, len(sizes))
	sampleIdx := 0
	for ci, base := range chunkOffsets {
		n := int(samplesPerChunk[ci])
		cursor := base
		for j := 0; j < n && sampleIdx < len(sizes); j++ {
			offsets[sampleIdx] = cursor
			chunkOf[sampleIdx] = ci
			cursor += uint64(sizes[sampleIdx])
			sampleIdx++
		}
	}
	for ; sampleIdx < len(sizes); sampleIdx++ {
		errs.Add(model.MediaError{
			Kind: model.KindSampleOutOfBounds, TrackID: t.ID, SampleIndex: sampleIdx,
			Detail: "sample not reachable from stsc/stco tables",
		})
	}
	return offsets, chunkOf
}
