package index

import (
	"fmt"
	"sort"

	"mp4seek/internal/bmff"
)

// Sample is one expanded sample-table row.
type Sample struct {
	Index  int
	Offset int64
	Size   uint32
	DTS    int64 // media timescale ticks
	CTO    int64 // signed composition offset
	PTS    int64 // DTS + CTO
	Key    bool
}

// Table is a fully expanded classic (stbl) sample table.
type Table struct {
	Samples []Sample
	Entry   SampleEntry
}

// BuildClassic expands the sample table under an stbl box. All offsets
// are validated against the file size and for overlap.
func BuildClassic(r *bmff.Reader, stbl bmff.Box) (*Table, error) {
	kids, err := r.Children(stbl)
	if err != nil {
		return nil, err
	}
	byType := map[string]bmff.Box{}
	for _, k := range kids {
		if _, dup := byType[k.Type]; !dup {
			byType[k.Type] = k
		}
	}
	load := func(typ string) (*cursor, error) {
		b, ok := byType[typ]
		if !ok {
			return nil, fmt.Errorf("%w: %s: missing %s", bmff.ErrCorrupt, stbl.Path, typ)
		}
		data, err := r.ReadBoxData(b, bmff.MaxMetaBox)
		if err != nil {
			return nil, err
		}
		return &cursor{data: data, path: b.Path}, nil
	}

	t := &Table{}
	// stsd: codec and decoder config.
	sc, err := load("stsd")
	if err != nil {
		return nil, err
	}
	if t.Entry, err = ParseStsd(sc.data, stbl.Path); err != nil {
		return nil, err
	}

	// stts: decode-time deltas.
	tc, err := load("stts")
	if err != nil {
		return nil, err
	}
	durations, err := expandStts(tc)
	if err != nil {
		return nil, err
	}

	// stsz / stz2: sample sizes.
	sizes, err := loadSizes(byType, r, stbl.Path)
	if err != nil {
		return nil, err
	}
	if len(sizes) != len(durations) {
		return nil, fmt.Errorf("%w: %s: stts implies %d samples, stsz/stz2 has %d",
			bmff.ErrCorrupt, stbl.Path, len(durations), len(sizes))
	}

	// stsc + stco/co64: sample offsets.
	offsets, err := sampleOffsets(byType, r, stbl.Path, sizes)
	if err != nil {
		return nil, err
	}
	if len(offsets) != len(sizes) {
		return nil, fmt.Errorf("%w: %s: stsc/stco imply %d samples, stsz has %d",
			bmff.ErrCorrupt, stbl.Path, len(offsets), len(sizes))
	}

	// ctts: composition offsets (signed for version 1).
	ctos, err := loadCtts(byType, r, stbl.Path, len(sizes))
	if err != nil {
		return nil, err
	}

	// stss: sync samples; absent means all key.
	keys, err := loadStss(byType, r, stbl.Path, len(sizes))
	if err != nil {
		return nil, err
	}

	t.Samples = make([]Sample, len(sizes))
	var dts int64
	for i := range t.Samples {
		s := Sample{Index: i, Offset: offsets[i], Size: sizes[i], DTS: dts, CTO: ctos[i], Key: keys[i]}
		s.PTS = s.DTS + s.CTO
		t.Samples[i] = s
		dts += int64(durations[i])
	}
	if err := validateRanges(t.Samples, r.Size(), stbl.Path); err != nil {
		return nil, err
	}
	return t, nil
}

func expandStts(c *cursor) ([]uint32, error) {
	if _, _, err := c.fullBoxHeader(); err != nil {
		return nil, err
	}
	count, err := c.u32()
	if err != nil {
		return nil, err
	}
	if count > MaxSamples {
		return nil, c.fail(fmt.Sprintf("stts entry count %d too large", count))
	}
	var out []uint32
	var total uint64
	for i := uint32(0); i < count; i++ {
		n, err := c.u32()
		if err != nil {
			return nil, err
		}
		delta, err := c.u32()
		if err != nil {
			return nil, err
		}
		total += uint64(n)
		if total > MaxSamples {
			return nil, c.fail(fmt.Sprintf("stts expands to %d samples", total))
		}
		for ; n > 0; n-- {
			out = append(out, delta)
		}
	}
	return out, nil
}

// validateRanges checks offset overflow, file bounds, and overlap.
func validateRanges(samples []Sample, fileSize int64, path string) error {
	sorted := make([]Sample, len(samples))
	copy(sorted, samples)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Offset < sorted[j].Offset })
	var prevEnd int64 = -1
	for _, s := range sorted {
		if s.Offset < 0 || int64(s.Size) < 0 || s.Offset > fileSize-int64(s.Size) {
			return fmt.Errorf("%w: %s: sample %d range [%d,%d) outside file of %d bytes",
				bmff.ErrCorrupt, path, s.Index, s.Offset, s.Offset+int64(s.Size), fileSize)
		}
		if prevEnd >= 0 && s.Offset < prevEnd {
			return fmt.Errorf("%w: %s: sample %d at %d overlaps previous sample ending %d",
				bmff.ErrCorrupt, path, s.Index, s.Offset, prevEnd)
		}
		prevEnd = s.Offset + int64(s.Size)
	}
	return nil
}
