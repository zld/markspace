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
	return nil, fmt.Errorf("sampleOffsets not implemented")
}

func loadCtts(byType map[string]bmff.Box, r *bmff.Reader, path string, n int) ([]int64, error) {
	return nil, fmt.Errorf("loadCtts not implemented")
}

func loadStss(byType map[string]bmff.Box, r *bmff.Reader, path string, n int) ([]bool, error) {
	return nil, fmt.Errorf("loadStss not implemented")
}
