package stream

import (
	"fmt"

	"mediasync/internal/clock"
	"mediasync/internal/issue"
)

// ---------------------------------------------------------------------------
// Sample table expansion

type sample struct {
	dts    clock.Tick
	pts    clock.Tick
	offset int64
	size   uint32
}

func expandSamples(t *trakData, fileSize int64) ([]sample, issue.List) {
	var issues issue.List
	if t.sttsFailed || t.stszFailed || t.stsc == nil || (t.stco == nil && t.co64 == nil) {
		// The box walk already reported the exact first damaged byte when a
		// table could not be read, and the structural scan localises a cut.
		// Do not invent coarser, duplicate problems for the same damage.
		return nil, issues
	}
	scale := t.mdhd.Timescale
	if scale == 0 {
		issues.Add(issue.New(issue.MP4Structure, "", "mdhd timescale is zero").WithByte(t.mdhdOffset))
		return nil, issues
	}

	// Decode times are cumulative and can exceed 32 bits after ~13 hours;
	// expand the 32-bit wrapped series once so duplicates/regressions are
	// caught in the shared model.
	unw := clock.NewUnwrapper(1 << 32)
	raw := uint64(0)
	var dtsRaw []uint64
	for _, e := range t.stts.Entries {
		for k := uint32(0); k < e.SampleCount; k++ {
			dtsRaw = append(dtsRaw, raw%(1<<32))
			raw += uint64(e.SampleDelta)
		}
	}
	dts := make([]clock.Tick, len(dtsRaw))
	for i, r := range dtsRaw {
		u := unw.Push(r)
		switch {
		case u.Duplicate:
			issues.Add(issue.New(issue.TimestampDuplicate, "",
				fmt.Sprintf("decode timestamp %d repeated at sample %d", r, i+1)).
				WithPacket(i + 1).WithByte(t.sttsOffset))
		case u.Regressed:
			issues.Add(issue.New(issue.TimestampRegress, "",
				fmt.Sprintf("decode timestamp went backwards at sample %d", i+1)).
				WithPacket(i + 1).WithByte(t.sttsOffset))
		}
		dts[i] = toTick(u.Value, scale)
	}

	cttsOffsets := make([]int64, len(dts))
	if t.ctts != nil {
		i := 0
		for _, e := range t.ctts.Entries {
			var v int64
			if t.ctts.GetVersion() == 1 {
				v = int64(e.SampleOffsetV1)
			} else {
				v = int64(e.SampleOffsetV0)
			}
			for k := uint32(0); k < e.SampleCount && i < len(cttsOffsets); k++ {
				cttsOffsets[i] = v
				i++
			}
		}
	}

	sizes := expandSizes(t, len(dts), &issues)
	offsets := expandOffsets(t, sizes, fileSize, &issues)

	n := len(dts)
	if len(sizes) < n {
		n = len(sizes)
	}
	if len(offsets) < n {
		n = len(offsets)
	}
	if len(sizes) != len(dts) || len(offsets) != len(dts) {
		issues.Add(issue.New(issue.MP4SampleTable, "",
			fmt.Sprintf("sample count mismatch: stts=%d stsz=%d stco=%d",
				len(dts), len(sizes), len(offsets))).WithByte(t.stszOffset))
	}
	samples := make([]sample, 0, n)
	for i := 0; i < n; i++ {
		samples = append(samples, sample{
			dts:    dts[i],
			pts:    dts[i] + toTick(uint64(absInt64(cttsOffsets[i])), scale)*sign(cttsOffsets[i]),
			offset: offsets[i],
			size:   sizes[i],
		})
	}
	return samples, issues
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func sign(v int64) clock.Tick {
	if v < 0 {
		return -1
	}
	return 1
}

func expandSizes(t *trakData, n int, issues *issue.List) []uint32 {
	out := make([]uint32, 0, n)
	if t.stsz == nil {
		issues.Add(issue.New(issue.MP4Structure, "", "missing stsz sample size table").WithByte(t.trakOffset))
		return nil
	}
	if t.stsz.SampleSize != 0 {
		for i := 0; i < n; i++ {
			out = append(out, t.stsz.SampleSize)
		}
		return out
	}
	if len(t.stsz.EntrySize) != n {
		issues.Add(issue.New(issue.MP4SampleTable, "",
			fmt.Sprintf("stsz has %d entries for %d samples", len(t.stsz.EntrySize), n)).WithByte(t.stszOffset))
	}
	out = append(out, t.stsz.EntrySize...)
	return out
}

func expandOffsets(t *trakData, sizes []uint32, fileSize int64, issues *issue.List) []int64 {
	if t.stsc == nil {
		issues.Add(issue.New(issue.MP4Structure, "", "missing stsc chunk table").WithByte(t.trakOffset))
		return nil
	}
	var chunkOffsets []int64
	switch {
	case t.stco != nil:
		for _, c := range t.stco.ChunkOffset {
			chunkOffsets = append(chunkOffsets, int64(c))
		}
	case t.co64 != nil:
		for _, c := range t.co64.ChunkOffset {
			chunkOffsets = append(chunkOffsets, int64(c))
		}
	default:
		issues.Add(issue.New(issue.MP4Structure, "", "missing stco/co64 chunk offsets").WithByte(t.trakOffset))
		return nil
	}
	var out []int64
	totalChunks := int64(len(chunkOffsets))
	for i, e := range t.stsc.Entries {
		first := int64(e.FirstChunk)
		last := totalChunks
		if i+1 < len(t.stsc.Entries) {
			last = int64(t.stsc.Entries[i+1].FirstChunk) - 1
		}
		if first < 1 || last > totalChunks || last < first {
			issues.Add(issue.New(issue.MP4SampleTable, "",
				fmt.Sprintf("stsc entry %d references chunks [%d,%d] of %d", i, first, last, totalChunks)).
				WithByte(t.trakOffset))
			continue
		}
		for c := first; c <= last; c++ {
			off := chunkOffsets[c-1]
			for k := uint32(0); k < e.SamplesPerChunk; k++ {
				if len(out) >= len(sizes) {
					break
				}
				out = append(out, off)
				if int64(len(out)) <= int64(len(sizes)) {
					off += int64(sizes[len(out)-1])
				}
			}
		}
	}
	for i, off := range out {
		if off < 0 || off+int64(sizes[i]) > fileSize {
			issues.Add(issue.New(issue.MP4Truncated, "",
				fmt.Sprintf("sample %d payload [%d,%d) exceeds file size %d", i+1, off, off+int64(sizes[i]), fileSize)).
				WithByte(off).WithPacket(i + 1))
			out[i] = fileSize
			if i < len(out) {
				break // the rest of the file is unusable
			}
		}
	}
	return out
}
