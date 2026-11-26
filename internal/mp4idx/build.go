package mp4idx

import (
	"io"
	"math"

	"github.com/abema/go-mp4"

	"mediamerge/internal/clock"
	"mediamerge/internal/fault"
)

// build reconstructs the per-sample table, verifying every byte offset against
// the real file size before accepting it. No scan ever runs past a box end.
func (rd *Reader) build(raw *rawTables) (*Index, error) {
	fileSize, err := rd.r.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, fault.New(fault.StageIO, fault.KindIO, rd.source, err.Error())
	}
	count := raw.stsz.SampleCount
	if raw.stsz.SampleSize != 0 && count == 0 {
		// Constant sample size: count must come from stts totals.
		count = sttsTotal(raw.stts)
	}
	sizes, err := sampleSizes(raw.stsz, count)
	if err != nil {
		return nil, err
	}
	dts, err := sampleDTS(raw, int(count))
	if err != nil {
		return nil, err
	}
	chunks, err := expandChunks(raw, int(count), sizes, fileSize)
	if err != nil {
		return nil, err
	}
	syncSet, rebuilt, unrecoverable, warns := resolveSync(raw, count)
	for _, w := range warns {
		_ = w // surfaced via index below
	}
	idx := &Index{Timescale: raw.timescale, SyncRebuilt: rebuilt, Unrecoverable: unrecoverable, Warnings: warns}
	for i := uint32(0); i < count; i++ {
		if len(dts) <= int(i) || len(chunks) <= int(i) {
			return nil, fault.New(fault.StageIndex, fault.KindIndex, rd.source,
				"sample table entries do not cover declared sample count")
		}
		tk, terr := clock.FromTimescale(dts[i], raw.timescale)
		if terr != nil {
			return nil, terr
		}
		num := i + 1
		isSync := syncSet[num]
		// With a valid stss (or no stss) every sample's sync state is proven.
		known := !unrecoverable
		if known && isSync {
			idx.SyncCount++
		}
		idx.Samples = append(idx.Samples, Sample{
			Number: num, Offset: chunks[i].offset, Size: sizes[i],
			DTS: tk, IsSync: isSync, SyncKnown: known,
		})
	}
	return idx, nil
}

func sttsTotal(stts *mp4.Stts) uint32 {
	var total uint32
	for _, e := range stts.Entries {
		total += e.SampleCount
	}
	return total
}

func sampleSizes(stsz *mp4.Stsz, count uint32) ([]uint32, error) {
	if stsz.SampleSize != 0 {
		out := make([]uint32, count)
		for i := range out {
			out[i] = stsz.SampleSize
		}
		return out, nil
	}
	if uint32(len(stsz.EntrySize)) < count {
		return nil, fault.New(fault.StageIndex, fault.KindIndex, "",
			"stsz entry list shorter than declared sample count")
	}
	return append([]uint32(nil), stsz.EntrySize[:count]...), nil
}

func sampleDTS(raw *rawTables, count int) ([]int64, error) {
	out := make([]int64, 0, count)
	var t int64
	produced := 0
	for _, e := range raw.stts.Entries {
		for k := uint32(0); k < e.SampleCount; k++ {
			if produced >= count {
				break
			}
			out = append(out, t)
			if t > math.MaxInt64-int64(e.SampleDelta) {
				return nil, fault.New(fault.StageIndex, fault.KindOverflow, "",
					"sample decode timestamp overflow")
			}
			t += int64(e.SampleDelta)
			produced++
		}
	}
	if produced < count {
		return nil, fault.New(fault.StageIndex, fault.KindIndex, "",
			"stts produces fewer samples than stsz declares")
	}
	return out, nil
}
