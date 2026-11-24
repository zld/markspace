package demux

import (
	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

// parseSampleTable resolves the classic (non-fragmented) sample tables into
// absolute sample offsets and exact decode/composition times.
func parseSampleTable(stbl *rawBox, timescale uint32, dataLen int) ([]Sample, error) {
	deltas, err := parseStts(stbl)
	if err != nil {
		return nil, err
	}
	sizes, err := parseStsz(stbl)
	if err != nil {
		return nil, err
	}
	offsets, err := parseChunkOffsets(stbl)
	if err != nil {
		return nil, err
	}
	entries, err := parseStsc(stbl)
	if err != nil {
		return nil, err
	}
	sampleOffsets := make([]int64, len(sizes))
	si := 0
	for ei, e := range entries {
		next := uint32(len(offsets)) + 1
		if ei+1 < len(entries) {
			next = entries[ei+1].firstChunk
		}
		first := e.firstChunk
		if first == 0 {
			first = 1
		}
		for chunk := first; chunk < next && int(chunk-1) < len(offsets); chunk++ {
			pos := offsets[chunk-1]
			for k := uint32(0); k < e.perChunk && si < len(sizes); k++ {
				sampleOffsets[si] = pos
				pos += int64(sizes[si])
				si++
			}
		}
	}
	ctts, err := parseCtts(stbl)
	if err != nil {
		return nil, err
	}
	syncs, hasStss, err := parseStss(stbl)
	if err != nil {
		return nil, err
	}
	samples := make([]Sample, len(sizes))
	var dts int64
	for i := range samples {
		var delta uint32
		if i < len(deltas) {
			delta = deltas[i]
		}
		var ctsOff int64
		if i < len(ctts) {
			ctsOff = int64(ctts[i])
		}
		sync := !hasStss || syncs[uint32(i+1)]
		off := sampleOffsets[i]
		if off < 0 || off+int64(sizes[i]) > int64(dataLen) {
			// A truncated tail keeps its verified prefix: stop at the first
			// sample the input cannot hold.
			samples = samples[:i]
			break
		}
		samples[i] = Sample{
			Offset: off,
			Size:   sizes[i],
			Delta:  delta,
			DTS:    clock.New(dts, timescale),
			CTS:    clock.New(dts+ctsOff, timescale),
			Sync:   sync,
			Data:   nil,
		}
		dts += int64(delta)
	}
	return samples, nil
}

func parseStts(stbl *rawBox) ([]uint32, error) {
	stts := stbl.find("stts")
	if stts == nil {
		return nil, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, stbl.payloadOff(), "missing stts")
	}
	r := newBinReader(stts, fault.OpIndexIncomplete)
	if _, _, err := r.fullBox(); err != nil {
		return nil, err
	}
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	var deltas []uint32
	for i := uint32(0); i < n; i++ {
		count, err := r.u32()
		if err != nil {
			return nil, err
		}
		delta, err := r.u32()
		if err != nil {
			return nil, err
		}
		for j := uint32(0); j < count; j++ {
			deltas = append(deltas, delta)
		}
	}
	return deltas, nil
}

func parseStsz(stbl *rawBox) ([]uint32, error) {
	stsz := stbl.find("stsz")
	if stsz == nil {
		return nil, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, stbl.payloadOff(), "missing stsz")
	}
	r := newBinReader(stsz, fault.OpIndexIncomplete)
	if _, _, err := r.fullBox(); err != nil {
		return nil, err
	}
	constant, err := r.u32()
	if err != nil {
		return nil, err
	}
	count, err := r.u32()
	if err != nil {
		return nil, err
	}
	sizes := make([]uint32, count)
	if constant != 0 {
		for i := range sizes {
			sizes[i] = constant
		}
		return sizes, nil
	}
	for i := range sizes {
		if sizes[i], err = r.u32(); err != nil {
			return nil, err
		}
	}
	return sizes, nil
}

func parseChunkOffsets(stbl *rawBox) ([]int64, error) {
	if stco := stbl.find("stco"); stco != nil {
		r := newBinReader(stco, fault.OpIndexIncomplete)
		if _, _, err := r.fullBox(); err != nil {
			return nil, err
		}
		n, err := r.u32()
		if err != nil {
			return nil, err
		}
		offsets := make([]int64, n)
		for i := range offsets {
			v, err := r.u32()
			if err != nil {
				return nil, err
			}
			offsets[i] = int64(v)
		}
		return offsets, nil
	}
	co64 := stbl.find("co64")
	if co64 == nil {
		return nil, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, stbl.payloadOff(), "missing stco/co64")
	}
	r := newBinReader(co64, fault.OpIndexIncomplete)
	if _, _, err := r.fullBox(); err != nil {
		return nil, err
	}
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	offsets := make([]int64, n)
	for i := range offsets {
		v, err := r.u64()
		if err != nil {
			return nil, err
		}
		offsets[i] = int64(v)
	}
	return offsets, nil
}

type stscEntry struct {
	firstChunk uint32
	perChunk   uint32
}

func parseStsc(stbl *rawBox) ([]stscEntry, error) {
	stsc := stbl.find("stsc")
	if stsc == nil {
		return nil, fault.New(fault.StageDemux, "mp4", fault.OpIndexIncomplete, stbl.payloadOff(), "missing stsc")
	}
	r := newBinReader(stsc, fault.OpIndexIncomplete)
	if _, _, err := r.fullBox(); err != nil {
		return nil, err
	}
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	entries := make([]stscEntry, n)
	for i := range entries {
		first, err := r.u32()
		if err != nil {
			return nil, err
		}
		per, err := r.u32()
		if err != nil {
			return nil, err
		}
		if err := r.skip(4); err != nil { // sample description index
			return nil, err
		}
		entries[i] = stscEntry{firstChunk: first, perChunk: per}
	}
	return entries, nil
}

func parseCtts(stbl *rawBox) ([]int32, error) {
	ctts := stbl.find("ctts")
	if ctts == nil {
		return nil, nil
	}
	r := newBinReader(ctts, fault.OpIndexIncomplete)
	version, _, err := r.fullBox()
	if err != nil {
		return nil, err
	}
	n, err := r.u32()
	if err != nil {
		return nil, err
	}
	var out []int32
	for i := uint32(0); i < n; i++ {
		count, err := r.u32()
		if err != nil {
			return nil, err
		}
		raw, err := r.u32()
		if err != nil {
			return nil, err
		}
		_ = version
		for j := uint32(0); j < count; j++ {
			out = append(out, int32(raw))
		}
	}
	return out, nil
}

// parseStss returns the set of 1-based sync sample numbers.  The bool result
// reports whether an stss box was present at all.
func parseStss(stbl *rawBox) (map[uint32]bool, bool, error) {
	stss := stbl.find("stss")
	if stss == nil {
		return nil, false, nil
	}
	r := newBinReader(stss, fault.OpIndexIncomplete)
	if _, _, err := r.fullBox(); err != nil {
		return nil, true, err
	}
	n, err := r.u32()
	if err != nil {
		return nil, true, err
	}
	set := make(map[uint32]bool, n)
	for i := uint32(0); i < n; i++ {
		v, err := r.u32()
		if err != nil {
			return nil, true, err
		}
		set[v] = true
	}
	return set, true, nil
}
