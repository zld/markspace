package index

import (
	"github.com/abema/go-mp4"

	"mediasync/internal/model"
)

// annotate replaces the class of an existing structured error while adding
// track and byte-offset context.
func annotate(err error, path string, trackID uint32, offset int64) error {
	me := model.AsMediaError(err)
	if me == nil {
		return err
	}
	cp := *me
	cp.Input = path
	cp.Track = trackID
	cp.Offset = offset
	return &cp
}

// expandChunks walks stsc first-chunk/samples-per-chunk entries together with
// stco/co64 chunk offsets and returns one contiguous file offset per sample.
func expandChunks(path string, tr TrackIndex, stscNode *boxNode, stsc *mp4.Stsc,
	stco *mp4.Stco, co64 *mp4.Co64, sizes []uint32, stbl *boxNode) ([]int64, error) {

	chunkOffsets := make([]int64, 0)
	switch {
	case co64 != nil:
		for _, o := range co64.ChunkOffset {
			chunkOffsets = append(chunkOffsets, int64(o))
		}
	case stco != nil:
		for _, o := range stco.ChunkOffset {
			chunkOffsets = append(chunkOffsets, int64(o))
		}
	}
	if len(stsc.Entries) == 0 {
		return nil, model.AtError(model.ClassCorruptBox, path, tr.TrackID,
			int64(stscNode.info.Offset), "empty stsc entries")
	}

	out := make([]int64, 0, len(sizes))
	sampleIdx := 0
	entryIdx := 0
	for chunk := uint32(0); chunk < uint32(len(chunkOffsets)); chunk++ {
		// Advance to the stsc entry governing this chunk.
		for entryIdx+1 < len(stsc.Entries) && stsc.Entries[entryIdx+1].FirstChunk-1 <= chunk {
			entryIdx++
		}
		samplesPerChunk := stsc.Entries[entryIdx].SamplesPerChunk
		cursor := chunkOffsets[chunk]
		for s := uint32(0); s < samplesPerChunk; s++ {
			if sampleIdx >= len(sizes) {
				return nil, model.AtError(model.ClassOutOfRange, path, tr.TrackID,
					int64(stscNode.info.Offset), "chunk %d references sample beyond stts/stsz", chunk)
			}
			if cursor < 0 {
				return nil, model.AtError(model.ClassOutOfRange, path, tr.TrackID,
					int64(stscNode.info.Offset), "negative sample offset in chunk %d", chunk)
			}
			out = append(out, cursor)
			cursor += int64(sizes[sampleIdx])
			sampleIdx++
		}
	}
	if sampleIdx != len(sizes) {
		return nil, model.AtError(model.ClassCorruptBox, path, tr.TrackID,
			int64(stbl.info.Offset), "chunks cover %d samples but stts/stsz declare %d", sampleIdx, len(sizes))
	}
	return out, nil
}
