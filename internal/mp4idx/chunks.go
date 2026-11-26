package mp4idx

import (
	"github.com/abema/go-mp4"

	"mediamerge/internal/fault"
)

type placedSample struct{ offset int64 }

// expandChunks applies the stsc run-length rules, places every sample inside
// its chunk contiguously and rejects any offset that leaves the file or makes
// integer addition overflow.
func expandChunks(raw *rawTables, count int, sizes []uint32, fileSize int64) ([]placedSample, error) {
	if len(raw.chunks) == 0 {
		return nil, fault.New(fault.StageIndex, fault.KindIndex, raw.source,
			"stco/co64 chunk offset table missing")
	}
	runs, err := chunkRuns(raw.stsc, len(raw.chunks))
	if err != nil {
		return nil, err
	}
	out := make([]placedSample, 0, count)
	sampleIdx := 0
	boxType := raw.offsetBoxType
	if boxType == "" {
		boxType = "stco"
	}
	for chunkNo, ch := range raw.chunks {
		perChunk := runs[chunkNo]
		cursor := int64(ch.offset)
		if cursor < 0 || uint64(cursor) > uint64(fileSize) {
			return nil, fault.AtOffset(fault.StageIndex, fault.KindBounds, raw.source, cursor,
				boxType+" chunk offset outside file bounds")
		}
		for s := 0; s < perChunk; s++ {
			if sampleIdx >= count {
				return nil, fault.New(fault.StageIndex, fault.KindIndex, raw.source,
					"stsc produces more samples than stsz declares")
			}
			size := int64(sizes[sampleIdx])
			end := cursor + size
			if end < cursor || end > fileSize {
				return nil, fault.AtOffset(fault.StageIndex, fault.KindBounds, raw.source, cursor,
					boxType+" sample extent runs past EOF")
			}
			out = append(out, placedSample{offset: cursor})
			cursor = end
			sampleIdx++
		}
	}
	if sampleIdx != count {
		return nil, fault.New(fault.StageIndex, fault.KindIndex, raw.source,
			"stsc/chunk layout does not cover every declared sample")
	}
	return out, nil
}

// chunkRuns expands stsc entries into a samples-per-chunk value per chunk.
func chunkRuns(stsc *mp4.Stsc, chunkCount int) ([]int, error) {
	if len(stsc.Entries) == 0 {
		return nil, fault.New(fault.StageIndex, fault.KindIndex, "", "empty stsc entry table")
	}
	out := make([]int, chunkCount)
	for i := range stsc.Entries {
		e := stsc.Entries[i]
		first := int(e.FirstChunk) // 1-based
		if first < 1 || first > chunkCount {
			return nil, fault.New(fault.StageIndex, fault.KindBounds, "",
				"stsc FirstChunk points outside the chunk table")
		}
		last := chunkCount
		if i+1 < len(stsc.Entries) {
			last = int(stsc.Entries[i+1].FirstChunk) - 1
		}
		for c := first - 1; c < last; c++ {
			out[c] = int(e.SamplesPerChunk)
		}
	}
	for c, n := range out {
		if n == 0 {
			return nil, fault.New(fault.StageIndex, fault.KindIndex, "",
				"stsc leaves chunk without a sample count")
		}
		_ = c
	}
	return out, nil
}
