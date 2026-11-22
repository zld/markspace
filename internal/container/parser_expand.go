package container

import (
	"archivist/internal/model"
)

// expandSamples turns chunk tables into per-sample Samples, validating every
// [offset, offset+size) range against the real file length. Dangling samples
// past EOF are trimmed only when truncated recovery is enabled.
func expandSamples(
	tr *model.Track,
	dts []uint64, durs, sizes []uint32,
	runs []chunkRun, offsets []uint64, fileLen uint64,
	opts ParseOptions,
) ([]model.Sample, int, error) {
	if len(dts) != len(sizes) {
		return nil, 0, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, tr.ID,
			"stts/stsz sample count mismatch")
	}
	chunkSamples, err := expandChunkRuns(runs, uint32(len(offsets)), uint32(len(sizes)))
	if err != nil {
		return nil, 0, err
	}
	out := make([]model.Sample, 0, len(sizes))
	sampleIdx := 0
	trimmed := 0
	for chunkIdx, chunkOff := range offsets {
		cur := chunkOff
		n := chunkSamples[chunkIdx]
		for j := 0; j < n; j++ {
			if sampleIdx >= len(sizes) {
				return nil, 0, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, tr.ID,
					"stsc expansion exceeds sample count")
			}
			sz := uint64(sizes[sampleIdx])
			if cur+sz > fileLen || cur+sz < cur {
				if opts.AllowTruncatedRecovery && isTailSample(sampleIdx, len(sizes)) {
					trimmed = len(sizes) - sampleIdx
					return finalizeTrimmed(out, tr, trimmed)
				}
				return nil, 0, model.AtSample(model.StageIndex, model.ErrOutOfBoundsOffset,
					tr.ID, uint32(sampleIdx), model.TimeRange{
						Start: dts[sampleIdx], End: dts[sampleIdx] + uint64(durs[sampleIdx]),
						Timescale: tr.Timescale,
					})
			}
			out = append(out, model.Sample{
				Index:    uint32(sampleIdx),
				DTS:      dts[sampleIdx],
				Duration: durs[sampleIdx],
				Offset:   cur,
				Size:     sizes[sampleIdx],
			})
			cur += sz
			sampleIdx++
		}
	}
	if sampleIdx != len(sizes) {
		return nil, 0, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, tr.ID,
			"stsc expansion does not cover all samples")
	}
	return out, 0, nil
}

func finalizeTrimmed(out []model.Sample, tr *model.Track, trimmed int) ([]model.Sample, int, error) {
	if len(out) == 0 {
		return nil, 0, model.AtTrack(model.StageIndex, model.ErrTruncatedBox, tr.ID,
			"truncated container has no readable samples")
	}
	return out, trimmed, nil
}

// isTailSample reports whether idx is at or past the final readable tail.
func isTailSample(idx, total int) bool { return idx >= total-1 }

// expandChunkRuns converts stsc first_chunk runs to a samples-per-chunk list.
func expandChunkRuns(runs []chunkRun, chunkCount, sampleCount uint32) ([]int, error) {
	out := make([]int, chunkCount)
	runIdx := 0
	consumed := 0
	for c := 0; c < int(chunkCount); c++ {
		// advance to the run covering chunk c (1-based c+1)
		for runIdx+1 < len(runs) && runs[runIdx+1].firstChunk <= uint32(c+1) {
			runIdx++
		}
		out[c] = int(runs[runIdx].samples)
		consumed += out[c]
		if consumed > int(sampleCount) {
			return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, 0,
				"stsc run expansion exceeds declared samples")
		}
	}
	return out, nil
}

// validateMonotonic enforces strictly increasing DTS within a track.
func validateMonotonic(tr *model.Track) error {
	for i := 1; i < len(tr.Samples); i++ {
		if tr.Samples[i].DTS <= tr.Samples[i-1].DTS {
			return model.AtSample(model.StageTimeline, model.ErrNonMonotonic,
				tr.ID, uint32(i), model.TimeRange{
					Start: tr.Samples[i-1].DTS, End: tr.Samples[i].DTS,
					Timescale: tr.Timescale,
				})
		}
	}
	return nil
}

// isMarkedKeyframe reads the last payload byte of a sample and checks the
// bitstream keyframe marker stamped by the fixture generator.
func isMarkedKeyframe(s model.Sample, data []byte) bool {
	if s.Size == 0 || uint64(len(data)) < s.Offset+uint64(s.Size) {
		return false
	}
	buf := data[s.Offset : s.Offset+uint64(s.Size)]
	return buf[len(buf)-1] == markerKeyframe
}

// rebuildKeyframes recovers keyframes from bitstream markers; sample 0 is
// always treated as a keyframe (random-access origin). It returns the count.
func rebuildKeyframes(samples []model.Sample, data []byte) int {
	n := 0
	for i := range samples {
		if i == 0 || isMarkedKeyframe(samples[i], data) {
			n++
		}
	}
	return n
}
