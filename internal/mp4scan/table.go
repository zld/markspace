package mp4scan

import (
	mp4 "github.com/abema/go-mp4"

	"mediakernel/internal/fault"
)

// buildTrack expands the stts/stsc/stco/stsz tables of one raw track into a
// per-sample table. Inconsistent tables are reported as ERR_CORRUPT_BOX at
// the container level (offset NA: the corruption is relational, not at one
// byte position).
func buildTrack(rt *rawTrack) (*Track, error) {
	if rt.stts == nil || rt.stsc == nil || rt.stsz == nil || len(rt.stco) == 0 {
		return nil, fault.New(fault.ErrCorruptBox, fault.NA,
			"track %d: incomplete sample table", rt.id).WithTrack(rt.id)
	}
	sizes, err := sampleSizes(rt.stsz)
	if err != nil {
		return nil, err
	}
	dts, err := decodeTimes(rt.stts, len(sizes))
	if err != nil {
		return nil, err
	}
	t := &Track{ID: rt.id, Timescale: rt.timescale}
	// Walk chunks; stsc rows give samples-per-chunk for chunk ranges.
	entries := rt.stsc.Entries
	sampleIdx := 0
	for chunk := 0; chunk < len(rt.stco); chunk++ {
		spc := samplesPerChunk(entries, chunk)
		off := rt.stco[chunk]
		for i := uint32(0); i < spc; i++ {
			if sampleIdx >= len(sizes) {
				return nil, fault.New(fault.ErrCorruptBox, fault.NA,
					"track %d: chunk table references sample %d beyond stsz (%d)",
					rt.id, sampleIdx, len(sizes)).WithTrack(rt.id)
			}
			sz := sizes[sampleIdx]
			t.Samples = append(t.Samples, Sample{
				Index:  uint32(sampleIdx),
				DTS:    dts[sampleIdx],
				Offset: int64(off),
				Size:   sz,
				Key:    len(rt.stss) == 0 || rt.stss[uint32(sampleIdx+1)],
			})
			off += uint64(sz)
			sampleIdx++
		}
	}
	if sampleIdx != len(sizes) {
		return nil, fault.New(fault.ErrCorruptBox, fault.NA,
			"track %d: stsc/stco cover %d samples, stsz has %d",
			rt.id, sampleIdx, len(sizes)).WithTrack(rt.id)
	}
	return t, nil
}

func sampleSizes(stsz *mp4.Stsz) ([]uint32, error) {
	n := int(stsz.SampleCount)
	sizes := make([]uint32, n)
	if stsz.SampleSize != 0 {
		for i := range sizes {
			sizes[i] = stsz.SampleSize
		}
		return sizes, nil
	}
	if len(stsz.EntrySize) < n {
		return nil, fault.New(fault.ErrCorruptBox, fault.NA,
			"stsz declares %d samples but lists %d sizes", n, len(stsz.EntrySize))
	}
	copy(sizes, stsz.EntrySize[:n])
	return sizes, nil
}

func decodeTimes(stts *mp4.Stts, count int) ([]int64, error) {
	dts := make([]int64, 0, count)
	var cur int64
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			dts = append(dts, cur)
			cur += int64(e.SampleDelta)
		}
	}
	if len(dts) < count {
		return nil, fault.New(fault.ErrCorruptBox, fault.NA,
			"stts expands to %d samples, stsz has %d", len(dts), count)
	}
	return dts[:count], nil
}

// samplesPerChunk resolves the stsc run that covers the given 0-based chunk.
func samplesPerChunk(entries []mp4.StscEntry, chunk int) uint32 {
	spc := uint32(0)
	for _, e := range entries {
		if int(e.FirstChunk)-1 <= chunk {
			spc = e.SamplesPerChunk
		} else {
			break
		}
	}
	return spc
}
