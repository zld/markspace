package index

import (
	"fmt"

	"mp4seek/internal/bmff"
)

func loadBox(r *bmff.Reader, byType map[string]bmff.Box, typ, path string) (*cursor, error) {
	b, ok := byType[typ]
	if !ok {
		return nil, fmt.Errorf("%w: %s: missing %s", bmff.ErrCorrupt, path, typ)
	}
	data, err := r.ReadBoxData(b, bmff.MaxMetaBox)
	if err != nil {
		return nil, err
	}
	return &cursor{data: data, path: b.Path}, nil
}

// loadSizes expands stsz or stz2 into per-sample sizes.
func loadSizes(byType map[string]bmff.Box, r *bmff.Reader, path string) ([]uint32, error) {
	if _, ok := byType["stz2"]; ok {
		return loadStz2(byType, r, path)
	}
	c, err := loadBox(r, byType, "stsz", path)
	if err != nil {
		return nil, err
	}
	if _, _, err := c.fullBoxHeader(); err != nil {
		return nil, err
	}
	uniform, err := c.u32()
	if err != nil {
		return nil, err
	}
	count, err := c.u32()
	if err != nil {
		return nil, err
	}
	if count > MaxSamples {
		return nil, c.fail(fmt.Sprintf("stsz count %d too large", count))
	}
	out := make([]uint32, 0, count)
	if uniform != 0 {
		for i := uint32(0); i < count; i++ {
			out = append(out, uniform)
		}
		return out, nil
	}
	if uint64(count)*4 > uint64(c.remaining()) {
		return nil, c.fail("stsz size table truncated")
	}
	for i := uint32(0); i < count; i++ {
		v, err := c.u32()
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func loadStz2(byType map[string]bmff.Box, r *bmff.Reader, path string) ([]uint32, error) {
	c, err := loadBox(r, byType, "stz2", path)
	if err != nil {
		return nil, err
	}
	if _, _, err := c.fullBoxHeader(); err != nil {
		return nil, err
	}
	if _, err := c.take(3); err != nil { // reserved
		return nil, err
	}
	fieldSize, err := c.u8()
	if err != nil {
		return nil, err
	}
	count, err := c.u32()
	if err != nil {
		return nil, err
	}
	if count > MaxSamples {
		return nil, c.fail(fmt.Sprintf("stz2 count %d too large", count))
	}
	out := make([]uint32, 0, count)
	switch fieldSize {
	case 4:
		for len(out) < int(count) {
			b, err := c.u8()
			if err != nil {
				return nil, err
			}
			out = append(out, uint32(b>>4))
			if len(out) < int(count) {
				out = append(out, uint32(b&0x0F))
			}
		}
	case 8:
		for len(out) < int(count) {
			b, err := c.u8()
			if err != nil {
				return nil, err
			}
			out = append(out, uint32(b))
		}
	case 16:
		for len(out) < int(count) {
			v, err := c.u16()
			if err != nil {
				return nil, err
			}
			out = append(out, uint32(v))
		}
	default:
		return nil, c.fail(fmt.Sprintf("stz2 field size %d unsupported", fieldSize))
	}
	return out, nil
}

// sampleOffsets combines stsc with stco/co64 into per-sample offsets.
func sampleOffsets(byType map[string]bmff.Box, r *bmff.Reader, path string, sizes []uint32) ([]int64, error) {
	var chunkOffsets []int64
	if _, ok := byType["co64"]; ok {
		c, err := loadBox(r, byType, "co64", path)
		if err != nil {
			return nil, err
		}
		if _, _, err := c.fullBoxHeader(); err != nil {
			return nil, err
		}
		n, err := c.u32()
		if err != nil {
			return nil, err
		}
		if uint64(n)*8 > uint64(c.remaining()) {
			return nil, c.fail("co64 truncated")
		}
		for i := uint32(0); i < n; i++ {
			v, err := c.u64()
			if err != nil {
				return nil, err
			}
			if v > uint64(1<<62) {
				return nil, c.fail(fmt.Sprintf("co64 offset %d overflows", v))
			}
			chunkOffsets = append(chunkOffsets, int64(v))
		}
	} else {
		c, err := loadBox(r, byType, "stco", path)
		if err != nil {
			return nil, err
		}
		if _, _, err := c.fullBoxHeader(); err != nil {
			return nil, err
		}
		n, err := c.u32()
		if err != nil {
			return nil, err
		}
		if uint64(n)*4 > uint64(c.remaining()) {
			return nil, c.fail("stco truncated")
		}
		for i := uint32(0); i < n; i++ {
			v, err := c.u32()
			if err != nil {
				return nil, err
			}
			chunkOffsets = append(chunkOffsets, int64(v))
		}
	}

	c, err := loadBox(r, byType, "stsc", path)
	if err != nil {
		return nil, err
	}
	if _, _, err := c.fullBoxHeader(); err != nil {
		return nil, err
	}
	n, err := c.u32()
	if err != nil {
		return nil, err
	}
	var entries []stscEntry
	for i := uint32(0); i < n; i++ {
		fc, err := c.u32()
		if err != nil {
			return nil, err
		}
		pc, err := c.u32()
		if err != nil {
			return nil, err
		}
		if _, err := c.u32(); err != nil { // sample description index
			return nil, err
		}
		if fc == 0 || (len(entries) > 0 && fc <= entries[len(entries)-1].firstChunk) {
			return nil, c.fail("stsc first_chunk not increasing")
		}
		entries = append(entries, stscEntry{fc, pc})
	}
	if len(entries) == 0 && len(chunkOffsets) > 0 {
		return nil, c.fail("stsc empty but chunks exist")
	}

	var out []int64
	si := 0 // index into sizes
	for chunk := 1; chunk <= len(chunkOffsets); chunk++ {
		ei := sort01(entries, uint32(chunk))
		perChunk := entries[ei].perChunk
		off := chunkOffsets[chunk-1]
		for j := uint32(0); j < perChunk; j++ {
			if si >= len(sizes) {
				return nil, fmt.Errorf("%w: %s: stsc references more samples than stsz provides",
					bmff.ErrCorrupt, path)
			}
			out = append(out, off)
			off += int64(sizes[si])
			if off < 0 { // int64 overflow guard
				return nil, fmt.Errorf("%w: %s: chunk offset overflow", bmff.ErrCorrupt, path)
			}
			si++
		}
	}
	return out, nil
}

type stscEntry struct{ firstChunk, perChunk uint32 }

// sort01 finds the last stsc entry whose firstChunk <= chunk.
func sort01(entries []stscEntry, chunk uint32) int {
	lo, hi := 0, len(entries)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if entries[mid].firstChunk <= chunk {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo
}

// loadCtts expands ctts; version 1 offsets are signed. Missing ctts
// yields zero offsets.
func loadCtts(byType map[string]bmff.Box, r *bmff.Reader, path string, n int) ([]int64, error) {
	out := make([]int64, n)
	if _, ok := byType["ctts"]; !ok {
		return out, nil
	}
	c, err := loadBox(r, byType, "ctts", path)
	if err != nil {
		return nil, err
	}
	version, _, err := c.fullBoxHeader()
	if err != nil {
		return nil, err
	}
	count, err := c.u32()
	if err != nil {
		return nil, err
	}
	i := 0
	for e := uint32(0); e < count; e++ {
		run, err := c.u32()
		if err != nil {
			return nil, err
		}
		var off int64
		if version == 1 {
			v, err := c.i32()
			if err != nil {
				return nil, err
			}
			off = int64(v)
		} else if version == 0 {
			v, err := c.u32()
			if err != nil {
				return nil, err
			}
			off = int64(v)
		} else {
			return nil, c.fail(fmt.Sprintf("ctts version %d unsupported", version))
		}
		for ; run > 0; run-- {
			if i >= n {
				return nil, c.fail("ctts expands beyond sample count")
			}
			out[i] = off
			i++
		}
	}
	if i != n {
		return nil, c.fail(fmt.Sprintf("ctts covers %d of %d samples", i, n))
	}
	return out, nil
}

// loadStss returns per-sample keyframe flags; all true when stss absent.
func loadStss(byType map[string]bmff.Box, r *bmff.Reader, path string, n int) ([]bool, error) {
	out := make([]bool, n)
	if _, ok := byType["stss"]; !ok {
		for i := range out {
			out[i] = true
		}
		return out, nil
	}
	c, err := loadBox(r, byType, "stss", path)
	if err != nil {
		return nil, err
	}
	if _, _, err := c.fullBoxHeader(); err != nil {
		return nil, err
	}
	count, err := c.u32()
	if err != nil {
		return nil, err
	}
	prev := uint32(0)
	for i := uint32(0); i < count; i++ {
		v, err := c.u32()
		if err != nil {
			return nil, err
		}
		if v == 0 || int(v) > n {
			return nil, c.fail(fmt.Sprintf("stss sample number %d out of range", v))
		}
		if v <= prev {
			return nil, c.fail("stss entries not increasing")
		}
		prev = v
		out[v-1] = true
	}
	return out, nil
}
