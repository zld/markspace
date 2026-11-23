package parse

import (
	"encoding/binary"

	"mediaforensics/internal/model"
)

// ExtractIndexes parses every trak's sample tables. A table defect on one
// track does not abort the others; the track's errors are attached.
func ExtractIndexes(data []byte, c *Container) ([]*TrackIndex, []*model.ForensicError) {
	var out []*TrackIndex
	var errs []*model.ForensicError
	moov, ok := Find(c, "moov")
	if !ok {
		return out, append(errs, model.NewError(model.StageParseBox, "moov", -1, -1, "no moov box"))
	}
	mdat, _ := findTopLevel(c, "mdat")
	for _, trak := range childrenOf(moov, "trak") {
		idx, idxErrs := extractTrack(data, trak, mdat)
		errs = append(errs, idxErrs...)
		out = append(out, idx)
	}
	return out, errs
}

func extractTrack(data []byte, trak, mdat BoxRecord) (*TrackIndex, []*model.ForensicError) {
	var errs []*model.ForensicError
	id := readTrackID(data, trak)
	idx := &TrackIndex{TrackID: id, EarliestOffset: -1}
	mdia, ok := childOf(trak, "mdia")
	if !ok {
		return idx, append(errs, model.NewError(model.StageParseBox, "mdia", id, trak.Offset, "trak has no mdia"))
	}
	if h, ok := childOf(mdia, "hdlr"); ok {
		idx.Kind = readHandlerType(data, h)
	}
	if m, ok := childOf(mdia, "mdhd"); ok {
		idx.Timescale = int64(binary.BigEndian.Uint32(data[m.Offset+m.HeaderSize+12:]))
	}
	stbl, ok := findNestedBox(trak, []string{"mdia", "minf", "stbl"})
	if !ok {
		return idx, append(errs, model.NewError(model.StageParseBox, "stbl", id, trak.Offset, "trak has no stbl"))
	}
	idx.readStts(data, stbl, &errs)
	idx.readStsz(data, stbl, &errs)
	idx.readStss(data, stbl, &errs)
	hasStco := idx.readStco(data, stbl, &errs)
	hasStsc := idx.readStsc(data, stbl, &errs)
	if !hasStco || !hasStsc {
		idx.rebuildGeometry(data, stbl, mdat, hasStco, hasStsc, &errs)
	} else {
		idx.ResolveOffsets(data, stbl, &errs)
		idx.markIntactIntervals()
	}
	return idx, errs
}

func (idx *TrackIndex) readStts(data []byte, stbl BoxRecord, errs *[]*model.ForensicError) {
	b, ok := childOf(stbl, "stts")
	if !ok {
		*errs = append(*errs, model.NewError(model.StageIndex, "stts_missing", idx.TrackID, stbl.Offset,
			"stts table absent"))
		return
	}
	p := b.Offset + b.HeaderSize + 4 // version/flags
	count, err := readU32(data, p)
	if err != nil {
		*errs = append(*errs, model.AsForensicError(err))
		return
	}
	if count < 1 || p+4+8 > b.Offset+b.Size {
		*errs = append(*errs, model.NewError(model.StageIndex, "stts_bounds", idx.TrackID, p,
			"stts entry count crosses box boundary"))
		return
	}
	idx.Delta = int64(binary.BigEndian.Uint32(data[p+8:]))
}

func (idx *TrackIndex) readStsz(data []byte, stbl BoxRecord, errs *[]*model.ForensicError) {
	b, ok := childOf(stbl, "stsz")
	if !ok {
		*errs = append(*errs, model.NewError(model.StageIndex, "stsz_missing", idx.TrackID, stbl.Offset,
			"stsz table absent; cannot rebuild boundaries"))
		return
	}
	p := b.Offset + b.HeaderSize + 4
	uniform := binary.BigEndian.Uint32(data[p:])
	count := binary.BigEndian.Uint32(data[p+4:])
	idx.SampleCount = int(count)
	idx.SampleSize = make([]int64, count)
	if uniform != 0 {
		for i := range idx.SampleSize {
			idx.SampleSize[i] = int64(uniform)
		}
		return
	}
	end := b.Offset + b.Size
	for i := uint32(0); i < count; i++ {
		ep := p + 8 + int64(i)*4
		if ep+4 > end {
			*errs = append(*errs, model.NewError(model.StageIndex, "stsz_bounds", idx.TrackID, ep,
				"stsz entry crosses box boundary"))
			idx.SampleCount = int(i)
			idx.SampleSize = idx.SampleSize[:i]
			return
		}
		idx.SampleSize[i] = int64(binary.BigEndian.Uint32(data[ep:]))
	}
}

func (idx *TrackIndex) readStss(data []byte, stbl BoxRecord, errs *[]*model.ForensicError) {
	idx.Keyframes = make([]bool, max1(idx.SampleCount))
	b, ok := childOf(stbl, "stss")
	if !ok {
		for i := range idx.Keyframes {
			idx.Keyframes[i] = true
		}
		return
	}
	p := b.Offset + b.HeaderSize + 4
	count, _ := readU32(data, p)
	for i := uint32(0); i < count; i++ {
		ep := p + 4 + int64(i)*4
		if ep+4 > b.Offset+b.Size {
			*errs = append(*errs, model.NewError(model.StageIndex, "stss_bounds", idx.TrackID, ep,
				"stss entry crosses box boundary"))
			return
		}
		n := binary.BigEndian.Uint32(data[ep:])
		if int(n) >= 1 && int(n) <= len(idx.Keyframes) {
			idx.Keyframes[n-1] = true
		}
	}
}
