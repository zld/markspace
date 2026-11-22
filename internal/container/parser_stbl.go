package container

import (
	"fmt"

	"archivist/internal/model"
)

func parseStbl(tr *model.Track, stbl *Box, fileLen uint64, opts ParseOptions) error {
	var (
		stts, stss, stsc, stsz, stco, co64 *Box
	)
	for _, child := range stbl.Children {
		switch child.Type {
		case BoxStts:
			stts = child
		case BoxStss:
			stss = child
		case BoxStsc:
			stsc = child
		case BoxStsz:
			stsz = child
		case BoxStco:
			stco = child
		case BoxCo64:
			co64 = child
		}
	}
	if stts == nil || stsc == nil || stsz == nil || (stco == nil && co64 == nil) {
		return model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, tr.ID,
			"stbl missing one of stts/stsc/stsz/stco")
	}
	dts, durs, err := parseStts(tr.ID, stts)
	if err != nil {
		return err
	}
	sizes, err := parseStsz(tr.ID, stsz)
	if err != nil {
		return err
	}
	chunks, err := parseChunks(tr.ID, stsc, len(sizes))
	if err != nil {
		return err
	}
	offsets, err := parseOffsets(tr.ID, stco, co64)
	if err != nil {
		return err
	}
	if len(chunks) != len(offsets) {
		return model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, tr.ID,
			fmt.Sprintf("stsc chunks=%d but offsets=%d", len(chunks), len(offsets)))
	}
	// Expand chunks into per-sample absolute offsets and validate every range.
	samples, trimmed, err := expandSamples(tr, dts, durs, sizes, chunks, offsets, fileLen, opts)
	if err != nil {
		return err
	}
	if uint32(len(samples)) > model.MaxSamples {
		return model.AtTrack(model.StageIndex, model.ErrLimitExceeded, tr.ID, "sample count")
	}
	// Keyframes.
	keySet := map[uint32]bool{}
	if stss != nil {
		keys, err := parseStss(tr.ID, stss, len(samples))
		if err != nil {
			return err
		}
		for _, k := range keys {
			keySet[k] = true
		}
		tr.IndexMode = "native"
	} else {
		if rebuildKeyframes(samples, opts.Data) == 0 {
			return model.AtTrack(model.StageIndex, model.ErrMissingKeyframeIdx, tr.ID,
				"no stss and no recoverable bitstream keyframe markers")
		}
		for i := range samples {
			if i == 0 || isMarkedKeyframe(samples[i], opts.Data) {
				keySet[uint32(i)] = true
			}
		}
		tr.IndexMode = "rebuilt"
	}
	for i := range samples {
		samples[i].IsKeyframe = keySet[uint32(i)]
	}
	if trimmed > 0 {
		tr.IndexMode = appendMode(tr.IndexMode, "trimmed")
	}
	tr.Samples = samples
	return validateMonotonic(tr)
}

func parseStts(trackID uint32, box *Box) ([]uint64, []uint32, error) {
	fb, ok := AsFullBox(box.Payload)
	if !ok || len(fb.Body) < 4 {
		return nil, nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "bad stts")
	}
	entries := U32(fb.Body[0:4])
	off := 4
	var dts []uint64
	var durs []uint32
	var t uint64
	for i := uint32(0); i < entries; i++ {
		if off+8 > len(fb.Body) {
			return nil, nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "stts run short")
		}
		count := U32(fb.Body[off : off+4])
		delta := U32(fb.Body[off+4 : off+8])
		off += 8
		if delta == 0 {
			return nil, nil, model.AtSample(model.StageTimeline, model.ErrNonMonotonic,
				trackID, uint32(len(dts)), model.TimeRange{Start: t, Timescale: 0})
		}
		for j := uint32(0); j < count; j++ {
			dts = append(dts, t)
			durs = append(durs, delta)
			t += uint64(delta)
		}
	}
	return dts, durs, nil
}

func parseStss(trackID uint32, box *Box, sampleCount int) ([]uint32, error) {
	fb, ok := AsFullBox(box.Payload)
	if !ok || len(fb.Body) < 4 {
		return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "bad stss")
	}
	count := U32(fb.Body[0:4])
	if 4+int(count)*4 > len(fb.Body) {
		return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "stss overruns box")
	}
	var keys []uint32
	prev := uint32(0)
	for i := uint32(0); i < count; i++ {
		oneBased := U32(fb.Body[4+i*4 : 8+i*4])
		if oneBased == 0 || int(oneBased) > sampleCount {
			return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID,
				fmt.Sprintf("stss entry %d out of range %d", oneBased, sampleCount))
		}
		idx := oneBased - 1
		if i > 0 && idx <= prev {
			return nil, model.AtSample(model.StageTimeline, model.ErrNonMonotonic,
				trackID, idx, model.TimeRange{})
		}
		prev = idx
		keys = append(keys, idx)
	}
	return keys, nil
}

func parseStsz(trackID uint32, box *Box) ([]uint32, error) {
	fb, ok := AsFullBox(box.Payload)
	if !ok || len(fb.Body) < 8 {
		return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "bad stsz")
	}
	uniform := U32(fb.Body[0:4])
	count := U32(fb.Body[4:8])
	if count > model.MaxSamples {
		return nil, model.AtTrack(model.StageIndex, model.ErrLimitExceeded, trackID, "stsz count")
	}
	sizes := make([]uint32, count)
	if uniform != 0 {
		for i := range sizes {
			sizes[i] = uniform
		}
		return sizes, nil
	}
	if 8+int(count)*4 > len(fb.Body) {
		return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "stsz overruns box")
	}
	for i := uint32(0); i < count; i++ {
		sizes[i] = U32(fb.Body[8+i*4 : 12+i*4])
		if sizes[i] == 0 {
			return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID,
				fmt.Sprintf("zero size at sample %d", i))
		}
	}
	return sizes, nil
}

func parseChunks(trackID uint32, box *Box, sampleCount int) ([]chunkRun, error) {
	fb, ok := AsFullBox(box.Payload)
	if !ok || len(fb.Body) < 4 {
		return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "bad stsc")
	}
	entries := U32(fb.Body[0:4])
	if 4+int(entries)*12 > len(fb.Body) {
		return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "stsc overruns")
	}
	var runs []chunkRun
	var total uint32
	for i := uint32(0); i < entries; i++ {
		first := U32(fb.Body[4+i*12 : 8+i*12])
		spc := U32(fb.Body[8+i*12 : 12+i*12])
		if first == 0 || spc == 0 {
			return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "zero stsc field")
		}
		runs = append(runs, chunkRun{firstChunk: first, samples: spc})
		total += spc
	}
	if int(total) > sampleCount*2 { // loose sanity; exact expansion checked later
		return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "stsc oversamples")
	}
	return runs, nil
}

func parseOffsets(trackID uint32, stco, co64 *Box) ([]uint64, error) {
	if co64 != nil {
		fb, ok := AsFullBox(co64.Payload)
		if !ok || len(fb.Body) < 4 {
			return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "bad co64")
		}
		count := U32(fb.Body[0:4])
		if 4+int(count)*8 > len(fb.Body) {
			return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "co64 overruns")
		}
		out := make([]uint64, count)
		for i := uint32(0); i < count; i++ {
			out[i] = U64(fb.Body[4+i*8 : 12+i*8])
		}
		return out, nil
	}
	fb, ok := AsFullBox(stco.Payload)
	if !ok || len(fb.Body) < 4 {
		return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "bad stco")
	}
	count := U32(fb.Body[0:4])
	if 4+int(count)*4 > len(fb.Body) {
		return nil, model.AtTrack(model.StageIndex, model.ErrCorruptSampleTable, trackID, "stco overruns")
	}
	out := make([]uint64, count)
	for i := uint32(0); i < count; i++ {
		out[i] = uint64(U32(fb.Body[4+i*4 : 8+i*4]))
	}
	return out, nil
}
