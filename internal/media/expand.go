package media

import (
	"fmt"

	"replay/internal/clock"
)

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
