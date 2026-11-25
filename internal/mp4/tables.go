package mp4

import (
	"fmt"

	gomp4 "github.com/abema/go-mp4"

	"mediacore/internal/clock"
)

func decodeDTS(r readAtSeeker, path string, track int, box *Box) ([]int64, error) {
	any, err := unmarshalBox(r, path, box, track)
	if err != nil {
		return nil, err
	}
	stts, ok := any.(*gomp4.Stts)
	if !ok {
		return nil, clock.New(clock.CategoryInvalidBox, path, track, box.Offset, "invalid stts")
	}
	var total uint32
	for _, e := range stts.Entries {
		total += e.SampleCount
	}
	out := make([]int64, 0, total)
	var dts int64
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			out = append(out, dts)
			dts += int64(e.SampleDelta)
		}
	}
	return out, nil
}

func decodePTS(r readAtSeeker, path string, track int, box *Box, dts []int64) ([]int64, error) {
	out := append([]int64(nil), dts...)
	if box == nil {
		return out, nil
	}
	any, err := unmarshalBox(r, path, box, track)
	if err != nil {
		return nil, err
	}
	ctts, ok := any.(*gomp4.Ctts)
	if !ok {
		return nil, clock.New(clock.CategoryInvalidBox, path, track, box.Offset, "invalid ctts")
	}
	idx := 0
	for _, e := range ctts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			if idx >= len(out) {
				return nil, clock.New(clock.CategoryInvalidBox, path, track, box.PayloadOffset, "ctts exceeds stts sample count")
			}
			off := int64(e.SampleOffsetV0)
			if ctts.GetVersion() == 1 {
				off = int64(e.SampleOffsetV1)
			}
			out[idx] += off
			idx++
		}
	}
	if idx != len(out) {
		return nil, clock.New(clock.CategoryInvalidBox, path, track, box.PayloadOffset, "ctts does not cover every sample")
	}
	return out, nil
}

func decodeSizes(r readAtSeeker, path string, track int, box *Box) ([]uint32, uint32, uint32, error) {
	any, err := unmarshalBox(r, path, box, track)
	if err != nil {
		return nil, 0, 0, err
	}
	stsz, ok := any.(*gomp4.Stsz)
	if !ok {
		return nil, 0, 0, clock.New(clock.CategoryInvalidBox, path, track, box.Offset, "invalid stsz")
	}
	if stsz.SampleSize != 0 {
		return nil, stsz.SampleSize, stsz.SampleCount, nil
	}
	if uint32(len(stsz.EntrySize)) != stsz.SampleCount {
		return nil, 0, 0, clock.New(clock.CategoryInvalidBox, path, track, box.PayloadOffset+8,
			"stsz entry count does not match sample_count")
	}
	return stsz.EntrySize, 0, stsz.SampleCount, nil
}

func decodeChunkOffsets(
	r readAtSeeker, path string, track int,
	stscBox, stcoBox, co64Box *Box,
	sampleCount uint32, sizes []uint32, uniformSize uint32,
) ([]int64, error) {
	scAny, err := unmarshalBox(r, path, stscBox, track)
	if err != nil {
		return nil, err
	}
	stsc, ok := scAny.(*gomp4.Stsc)
	if !ok || len(stsc.Entries) == 0 {
		return nil, clock.New(clock.CategoryInvalidBox, path, track, stscBox.Offset, "invalid or empty stsc")
	}
	var chunks []uint64
	if co64Box != nil {
		any, err := unmarshalBox(r, path, co64Box, track)
		if err != nil {
			return nil, err
		}
		co64, ok := any.(*gomp4.Co64)
		if !ok {
			return nil, clock.New(clock.CategoryInvalidBox, path, track, co64Box.Offset, "invalid co64")
		}
		chunks = co64.ChunkOffset
	} else {
		any, err := unmarshalBox(r, path, stcoBox, track)
		if err != nil {
			return nil, err
		}
		stco, ok := any.(*gomp4.Stco)
		if !ok {
			return nil, clock.New(clock.CategoryInvalidBox, path, track, stcoBox.Offset, "invalid stco")
		}
		for _, o := range stco.ChunkOffset {
			chunks = append(chunks, uint64(o))
		}
	}
	if len(chunks) == 0 {
		return nil, clock.New(clock.CategoryInvalidBox, path, track, stcoBox.Offset, "no chunk offsets")
	}

	out := make([]int64, sampleCount)
	sampleIdx := 0
	entryIdx := 0
	for chunk := uint32(1); chunk <= uint32(len(chunks)); chunk++ {
		for entryIdx+1 < len(stsc.Entries) && stsc.Entries[entryIdx+1].FirstChunk <= chunk {
			entryIdx++
		}
		entry := stsc.Entries[entryIdx]
		if entry.FirstChunk > chunk {
			return nil, clock.New(clock.CategoryInvalidBox, path, track, stscBox.PayloadOffset,
				fmt.Sprintf("stsc has no samples-per-chunk rule for chunk %d", chunk))
		}
		cursor := int64(chunks[chunk-1])
		for j := uint32(0); j < entry.SamplesPerChunk; j++ {
			if sampleIdx >= int(sampleCount) {
				return nil, clock.New(clock.CategoryInvalidBox, path, track, stscBox.PayloadOffset,
					"stsc describes more samples than stsz")
			}
			out[sampleIdx] = cursor
			size := uniformSize
			if size == 0 {
				size = sizes[sampleIdx]
			}
			cursor += int64(size)
			sampleIdx++
		}
	}
	if sampleIdx != int(sampleCount) {
		return nil, clock.New(clock.CategoryInvalidBox, path, track, stscBox.PayloadOffset,
			fmt.Sprintf("stsc/chunks cover %d of %d samples", sampleIdx, sampleCount))
	}
	return out, nil
}
