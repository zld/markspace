package mp4mux

import (
	"sort"

	"mediarun/internal/index"
)

func ptsList(samples []Sample) []uint64 {
	out := make([]uint64, len(samples))
	for i, s := range samples {
		out[i] = s.PTS
	}
	return out
}

func sampleOffsets(samples []Sample, chunkStart int64) []int64 {
	out := make([]int64, len(samples))
	off := chunkStart
	for i, s := range samples {
		out[i] = off
		off += int64(len(s.payload()))
	}
	return out
}

func sizeList(samples []Sample) []uint32 {
	out := make([]uint32, len(samples))
	for i, s := range samples {
		out[i] = uint32(len(s.payload()))
	}
	return out
}

// keyframeIndex resolves each keyframe's absolute payload offset and digest.
func keyframeIndex(samples []Sample, chunkStart int64) []index.Entry {
	offsets := make([]int64, len(samples))
	off := chunkStart
	for i, s := range samples {
		offsets[i] = off
		off += int64(len(s.payload()))
	}
	out := make([]index.Entry, 0, len(samples)/4+1)
	for i, s := range samples {
		if !s.Keyframe {
			continue
		}
		out = append(out, index.Entry{
			Sample:      i + 1,
			Track:       1,
			PTS:         s.PTS,
			SourceOff:   s.SourceOff,
			SourceHash:  s.SourceHash,
			PayloadHash: index.PayloadHash(s.payload()),
			DataOffset:  offsets[i],
			Size:        len(s.payload()),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sample < out[j].Sample })
	return out
}
