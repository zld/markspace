package probe

import "fmt"

func parseOffsets(f readerAt, stco, co64 *boxNode) ([]int64, error) {
	if stco != nil {
		b, err := readNodePayload(f, stco)
		if err != nil {
			return nil, err
		}
		if err := needBytes(b, 0, 8); err != nil {
			return nil, err
		}
		count := int(u32(b, 4))
		out := make([]int64, count)
		for i := range out {
			off := 8 + i*4
			if err := needBytes(b, off, 4); err != nil {
				return nil, fmt.Errorf("stco entry %d: %w", i, err)
			}
			out[i] = int64(u32(b, off))
		}
		return out, nil
	}
	b, err := readNodePayload(f, co64)
	if err != nil {
		return nil, err
	}
	count := int(u32(b, 4))
	out := make([]int64, count)
	for i := range out {
		off := 8 + i*8
		if err := needBytes(b, off, 8); err != nil {
			return nil, fmt.Errorf("co64 entry %d: %w", i, err)
		}
		out[i] = int64(u64(b, off))
	}
	return out, nil
}

func assignOffsets(samples []Sample, sizes []uint32, chunks []int64, entries []stscEntry) error {
	if len(samples) == 0 {
		return nil
	}
	if len(chunks) == 0 {
		return fmt.Errorf("no chunk offsets")
	}
	cursor := 0
	for chunkIndex := 1; chunkIndex <= len(chunks); chunkIndex++ {
		spc := 1
		if len(entries) > 0 {
			spc = samplesPerChunk(entries, chunkIndex)
		}
		offset := chunks[chunkIndex-1]
		for j := 0; j < spc && cursor < len(samples); j++ {
			samples[cursor].Offset = offset
			offset += int64(sizes[cursor])
			cursor++
		}
	}
	if cursor != len(samples) {
		return fmt.Errorf("stsc/stco assigned %d of %d samples", cursor, len(samples))
	}
	return nil
}

func samplesPerChunk(entries []stscEntry, chunk int) int {
	current := entries[0].SamplesPerChunk
	for _, e := range entries {
		if int(e.FirstChunk) <= chunk {
			current = e.SamplesPerChunk
		} else {
			break
		}
	}
	if current == 0 {
		return 1
	}
	return int(current)
}
