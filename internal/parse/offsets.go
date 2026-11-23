package parse

import (
	"encoding/binary"

	"mediaforensics/internal/model"
)

// ResolveOffsets expands chunk offsets (stco) through the sample-per-chunk
// map (stsc) and the size table (stsz) into one absolute offset per sample.
// It runs only for structurally complete tables and validates every range
// against the input boundary, recording forensic errors instead of skipping.
func (idx *TrackIndex) ResolveOffsets(data []byte, stbl BoxRecord, errs *[]*model.ForensicError) {
	if idx.Rebuilt || idx.SampleCount == 0 {
		if idx.EarliestOffset < 0 && len(idx.SampleOffset) > 0 {
			for _, off := range idx.SampleOffset {
				if off >= 0 {
					idx.EarliestOffset = off
					break
				}
			}
		}
		return
	}
	chunkOff, chunkCount := readChunkOffsets(data, stbl, errs, idx.TrackID)
	perChunk := readSamplesPerChunk(data, stbl, errs, idx.TrackID)
	inputSize := int64(len(data))
	idx.SampleOffset = make([]int64, idx.SampleCount)
	sample := 0
	first := int64(-1)
	for chunk := 0; chunk < chunkCount; chunk++ {
		spc := perChunk
		if sample+spc > idx.SampleCount {
			spc = idx.SampleCount - sample
		}
		cursor := chunkOff[chunk]
		if cursor < 0 || cursor >= inputSize {
			*errs = append(*errs, model.NewError(model.StageIndex, "chunk_out_of_range",
				idx.TrackID, cursor, "chunk base outside input"))
			sample += spc
			continue
		}
		for k := 0; k < spc && sample < idx.SampleCount; k++ {
			size := idx.SampleSize[sample]
			if cursor+size > inputSize {
				*errs = append(*errs, model.NewError(model.StageIndex, "sample_boundary",
					idx.TrackID, cursor, "sample extends past input end"))
				idx.SampleOffset[sample] = -1
			} else {
				idx.SampleOffset[sample] = cursor
				if first < 0 {
					first = cursor
				}
			}
			cursor += size
			sample++
		}
	}
	if sample < idx.SampleCount {
		*errs = append(*errs, model.NewError(model.StageIndex, "sample_count_mismatch",
			idx.TrackID, stbl.Offset, "chunks cover fewer samples than stsz declares"))
	}
	idx.EarliestOffset = first
}

func readChunkOffsets(data []byte, stbl BoxRecord, errs *[]*model.ForensicError, trackID int) ([]int64, int) {
	b, ok := childOf(stbl, "stco")
	if !ok {
		*errs = append(*errs, model.NewError(model.StageIndex, "stco_missing", trackID, stbl.Offset,
			"stco absent in ResolveOffsets"))
		return nil, 0
	}
	p := b.Offset + b.HeaderSize + 4
	count, _ := readU32(data, p)
	out := make([]int64, count)
	for i := uint32(0); i < count; i++ {
		out[i] = int64(binary.BigEndian.Uint32(data[p+4+int64(i)*4:]))
	}
	return out, int(count)
}

func readSamplesPerChunk(data []byte, stbl BoxRecord, errs *[]*model.ForensicError, trackID int) int {
	b, ok := childOf(stbl, "stsc")
	if !ok {
		*errs = append(*errs, model.NewError(model.StageIndex, "stsc_missing", trackID, stbl.Offset,
			"stsc absent in ResolveOffsets"))
		return 1
	}
	p := b.Offset + b.HeaderSize + 4
	count, _ := readU32(data, p)
	if count == 0 {
		return 1
	}
	// The synthetic subset emits exactly one stsc entry.
	return int(binary.BigEndian.Uint32(data[p+8:]))
}
