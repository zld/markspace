package container

import (
	"rtptrace/internal/clock"
	"rtptrace/internal/fault"
)

// fullBoxPrefix is version(1 byte) + flags(3 bytes) before every sample table.
const fullBoxPrefix = 4

func entryOffset(ref tableRef, index, entrySize uint64) int64 {
	return int64(ref.boxOffset + ref.headerSize + fullBoxPrefix + index*entrySize)
}

// buildSamples turns the raw sample tables into validated Samples. Every
// failure points at the first bad table entry rather than skipping it.
func buildSamples(tr *Track, rt *rawTrack, c *Container) error {
	field := func(name string) string { return "trak[" + itoa(tr.Index) + "]." + name }
	if rt.stts == nil {
		return fault.New(fault.KindCorrupt, field("stts"), fault.NoOffset, "missing stts box")
	}
	if rt.stsz == nil {
		return fault.New(fault.KindCorrupt, field("stsz"), fault.NoOffset, "missing stsz box")
	}
	if rt.stsc == nil {
		return fault.New(fault.KindCorrupt, field("stsc"), fault.NoOffset, "missing stsc box")
	}
	if len(rt.offsets) == 0 {
		return fault.New(fault.KindCorrupt, field("stco"), fault.NoOffset, "missing or empty chunk offset table")
	}

	dts, err := expandDTS(tr.Index, rt)
	if err != nil {
		return err
	}
	sizes, err := expandSizes(tr.Index, rt)
	if err != nil {
		return err
	}
	if len(dts) != len(sizes) {
		return fault.Newf(fault.KindCorrupt, field("stts"),
			entryOffset(rt.sttsRef, 0, 8),
			"stts expands to %d samples but stsz declares %d", len(dts), len(sizes))
	}
	chunks, err := expandChunks(tr.Index, rt, len(sizes))
	if err != nil {
		return err
	}
	// Validate chunk offsets themselves before walking samples, so a bad stco
	// entry is reported at its own position rather than at the first sample.
	for ci, off := range rt.offsets {
		if off >= c.FileSize {
			return fault.Newf(fault.KindCorrupt, field(chunkOffsetName(rt)),
				entryOffset(rt.coRef, uint64(ci), chunkEntrySize(rt)),
				"chunk %d offset %d exceeds file size %d", ci, off, c.FileSize).WithTrack(tr.Index)
		}
	}
	sync, err := expandSync(tr.Index, rt, len(sizes))
	if err != nil {
		return err
	}

	samples := make([]Sample, len(sizes))
	si := 0
	for ci, chunk := range chunks {
		off := rt.offsets[ci]
		for k := 0; k < chunk.samples; k++ {
			size := sizes[si]
			end, ok := checkedAdd(off, size)
			if !ok || end > c.FileSize {
				return fault.Newf(fault.KindCorrupt, field("stsz.entry_size"),
					entryOffset(rt.stszRef, uint64(si), 4),
					"sample %d [%d,%d) exceeds file size %d", si, off, end, c.FileSize)
			}
			if !insideMdat(off, end, c.Mdat) {
				return fault.Newf(fault.KindCorrupt, field(chunkOffsetName(rt)),
					entryOffset(rt.coRef, uint64(ci), chunkEntrySize(rt)),
					"sample %d at chunk %d [%d,%d) is outside any mdat", si, ci, off, end)
			}
			samples[si] = Sample{
				Index:  si,
				Offset: off,
				Size:   size,
				DTS:    dts[si],
				Sync:   sync[si],
			}
			off = end
			si++
		}
	}
	tr.Samples = samples
	tr.SampleCount = len(samples)
	return nil
}

type chunkSpec struct {
	samples int
}

func expandDTS(track int, rt *rawTrack) ([]uint64, error) {
	rate := clock.Rational{Num: 1, Den: uint64(rt.timescale)}
	var out []uint64
	var cur uint64
	for i, e := range rt.stts.Entries {
		if e.SampleCount == 0 {
			return nil, fault.Newf(fault.KindCorrupt, "stts.entry.sample_count",
				entryOffset(rt.sttsRef, uint64(i), 8),
				"zero sample count in stts entry %d", i).WithTrack(track)
		}
		for k := uint32(0); k < e.SampleCount; k++ {
			out = append(out, cur)
			next, err := clock.Add(
				clock.Duration{Value: cur, Rate: rate},
				clock.Duration{Value: uint64(e.SampleDelta), Rate: rate},
				"stts.dts",
			)
			if err != nil {
				return nil, err.(*fault.Error).WithTrack(track)
			}
			cur = next.Value
		}
	}
	return out, nil
}

func expandSizes(track int, rt *rawTrack) ([]uint64, error) {
	n := int(rt.stsz.SampleCount)
	if rt.stsz.SampleSize != 0 {
		out := make([]uint64, n)
		for i := range out {
			out[i] = uint64(rt.stsz.SampleSize)
		}
		return out, nil
	}
	if len(rt.stsz.EntrySize) != n {
		return nil, fault.Newf(fault.KindCorrupt, "stsz.entry_size",
			entryOffset(rt.stszRef, 0, 4),
			"sample_count=%d but entry_size has %d elements", n, len(rt.stsz.EntrySize)).WithTrack(track)
	}
	out := make([]uint64, n)
	for i, s := range rt.stsz.EntrySize {
		if s == 0 {
			return nil, fault.Newf(fault.KindCorrupt, "stsz.entry_size",
				entryOffset(rt.stszRef, uint64(i), 4),
				"zero size for sample %d", i).WithTrack(track)
		}
		out[i] = uint64(s)
	}
	return out, nil
}

func expandChunks(track int, rt *rawTrack, totalSamples int) ([]chunkSpec, error) {
	chunkCount := len(rt.offsets)
	perChunk := make([]int, chunkCount)
	var prevFirst uint32
	for i, e := range rt.stsc.Entries {
		if e.FirstChunk < 1 || (i > 0 && e.FirstChunk <= prevFirst) {
			return nil, fault.Newf(fault.KindCorrupt, "stsc.first_chunk",
				entryOffset(rt.stscRef, uint64(i), 12),
				"non-monotonic first_chunk=%d", e.FirstChunk).WithTrack(track)
		}
		if e.SamplesPerChunk == 0 {
			return nil, fault.Newf(fault.KindCorrupt, "stsc.samples_per_chunk",
				entryOffset(rt.stscRef, uint64(i), 12),
				"zero samples per chunk at entry %d", i).WithTrack(track)
		}
		start := int(e.FirstChunk) - 1
		stop := chunkCount
		if i+1 < len(rt.stsc.Entries) {
			stop = int(rt.stsc.Entries[i+1].FirstChunk) - 1
		}
		if start >= chunkCount {
			break
		}
		for ci := start; ci < stop; ci++ {
			perChunk[ci] = int(e.SamplesPerChunk)
		}
		prevFirst = e.FirstChunk
	}
	total := 0
	specs := make([]chunkSpec, chunkCount)
	for ci, n := range perChunk {
		if n == 0 {
			return nil, fault.Newf(fault.KindCorrupt, "stsc",
				entryOffset(rt.stscRef, 0, 12),
				"chunk %d has no stsc entry covering it", ci).WithTrack(track)
		}
		specs[ci] = chunkSpec{samples: n}
		total += n
	}
	if total != totalSamples {
		return nil, fault.Newf(fault.KindCorrupt, "stsc",
			entryOffset(rt.stscRef, 0, 12),
			"stsc covers %d samples but stsz declares %d", total, totalSamples).WithTrack(track)
	}
	return specs, nil
}

func expandSync(track int, rt *rawTrack, n int) ([]bool, error) {
	sync := make([]bool, n)
	if rt.stss == nil {
		for i := range sync {
			sync[i] = true
		}
		return sync, nil
	}
	for i, sn := range rt.stss.SampleNumber {
		if sn < 1 || int(sn) > n {
			return nil, fault.Newf(fault.KindCorrupt, "stss.sample_number",
				entryOffset(rt.stssRef, uint64(i), 4),
				"sync sample number %d out of range [1,%d]", sn, n).WithTrack(track)
		}
		sync[sn-1] = true
	}
	return sync, nil
}

func insideMdat(start, end uint64, ranges []MdatRange) bool {
	for _, m := range ranges {
		if start >= m.PayloadStart && end <= m.PayloadEnd {
			return true
		}
	}
	return false
}

func checkedAdd(a, b uint64) (uint64, bool) {
	if a > ^uint64(0)-b {
		return 0, false
	}
	return a + b, true
}

func chunkEntrySize(rt *rawTrack) uint64 {
	if rt.co64 {
		return 8
	}
	return 4
}

func chunkOffsetName(rt *rawTrack) string {
	if rt.co64 {
		return "co64.chunk_offset"
	}
	return "stco.chunk_offset"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
