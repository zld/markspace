package parse

import (
	"encoding/binary"

	"mediaforensics/internal/model"
)

// readStco loads chunk offsets and rejects entries pointing outside the input.
// Returns false when the box is absent.
func (idx *TrackIndex) readStco(data []byte, stbl BoxRecord, errs *[]*model.ForensicError) bool {
	b, ok := childOf(stbl, "stco")
	if !ok {
		return false
	}
	p := b.Offset + b.HeaderSize + 4
	count, err := readU32(data, p)
	if err != nil {
		*errs = append(*errs, model.AsForensicError(err))
		return true
	}
	if p+4+int64(count)*4 > b.Offset+b.Size {
		*errs = append(*errs, model.NewError(model.StageIndex, "stco_bounds", idx.TrackID, p,
			"stco entry count crosses box boundary"))
		return true
	}
	inputSize := int64(len(data))
	for i := uint32(0); i < count; i++ {
		ep := p + 4 + int64(i)*4
		off := int64(binary.BigEndian.Uint32(data[ep:]))
		if off < 0 || off >= inputSize {
			*errs = append(*errs, model.NewError(model.StageIndex, "stco_out_of_range",
				idx.TrackID, ep, fmtOffset(off)))
		}
	}
	return true
}

// readStsc validates the sample-to-chunk map. Returns false when absent.
func (idx *TrackIndex) readStsc(data []byte, stbl BoxRecord, errs *[]*model.ForensicError) bool {
	b, ok := childOf(stbl, "stsc")
	if !ok {
		return false
	}
	p := b.Offset + b.HeaderSize + 4
	count, _ := readU32(data, p)
	if p+4+int64(count)*12 > b.Offset+b.Size {
		*errs = append(*errs, model.NewError(model.StageIndex, "stsc_bounds", idx.TrackID, p,
			"stsc entry count crosses box boundary"))
		return true
	}
	first := binary.BigEndian.Uint32(data[p+4:])
	if first > 1 && count >= 1 {
		*errs = append(*errs, model.NewError(model.StageIndex, "stsc_first_chunk",
			idx.TrackID, p+4, "first_chunk does not start at 1"))
	}
	return true
}

// rebuildGeometry reconstructs sample offsets when stco and/or stsc are
// missing. mdat is interleaved across tracks, so offsets cannot be derived
// from mdat start + sizes alone: instead the synthetic MFSM payload headers
// are scanned to locate every sample belonging to this track. Each located
// sample is cross-checked against its stsz size. Intervals are marked
// medium confidence and record the scan as their basis.
func (idx *TrackIndex) rebuildGeometry(data []byte, stbl, mdat BoxRecord, hasStco, hasStsc bool, errs *[]*model.ForensicError) {
	idx.Rebuilt = true
	if mdat.Type != "mdat" {
		*errs = append(*errs, model.NewError(model.StageIndex, "mdat_missing", idx.TrackID, -1,
			"cannot rebuild without mdat"))
		return
	}
	if idx.SampleCount == 0 || len(idx.SampleSize) == 0 {
		*errs = append(*errs, model.NewError(model.StageIndex, "rebuild_empty", idx.TrackID, stbl.Offset,
			"no sample sizes available for rebuild"))
		return
	}
	idx.SampleOffset = make([]int64, idx.SampleCount)
	for i := range idx.SampleOffset {
		idx.SampleOffset[i] = -1
	}
	base := mdat.Offset + mdat.HeaderSize
	end := mdat.Offset + mdat.Size
	wantIndex := 0
	first := int64(-1)
	for cursor := base; cursor+20 <= end && wantIndex < idx.SampleCount; {
		if string(data[cursor:cursor+4]) != "MFSM" {
			cursor++
			continue
		}
		track := int64(binary.BigEndian.Uint32(data[cursor+4:]))
		sampleNo := int64(binary.BigEndian.Uint32(data[cursor+8:]))
		if track != int64(idx.TrackID) {
			cursor++
			continue
		}
		if sampleNo != int64(wantIndex) {
			*errs = append(*errs, model.NewError(model.StageIndex, "rebuild_gap",
				idx.TrackID, cursor, "non-contiguous sample sequence while scanning mdat"))
			cursor++
			continue
		}
		size := idx.SampleSize[wantIndex]
		if cursor+size > end {
			*errs = append(*errs, model.NewError(model.StageIndex, "rebuild_boundary",
				idx.TrackID, cursor, "rebuilt sample would cross mdat end"))
			break
		}
		idx.SampleOffset[wantIndex] = cursor
		if first < 0 {
			first = cursor
		}
		wantIndex++
		cursor += size
	}
	idx.EarliestOffset = first
	switch {
	case wantIndex == idx.SampleCount:
		idx.addInterval(0, idx.SampleCount-1, model.ConfidenceMedium,
			"MFSM header scan + stsz after missing "+missingWhich(hasStco, hasStsc))
	case wantIndex > 0:
		idx.addInterval(0, wantIndex-1, model.ConfidenceMedium,
			"partial MFSM header scan; remaining samples unverified")
	}
	*errs = append(*errs, model.NewError(model.StageIndex, "rebuilt", idx.TrackID, first,
		"index rebuilt from sample boundaries"))
}

// markIntactIntervals records high-confidence coverage when all tables exist.
func (idx *TrackIndex) markIntactIntervals() {
	if idx.SampleCount == 0 {
		return
	}
	idx.addInterval(0, idx.SampleCount-1, model.ConfidenceHigh, "complete stco/stsc/stsz/stts")
}

func (idx *TrackIndex) addInterval(first, last int, confidence, basis string) {
	if first > last {
		return
	}
	idx.Intervals = append(idx.Intervals, model.Interval{
		TrackID:     idx.TrackID,
		FirstSample: first,
		LastSample:  last,
		Confidence:  confidence,
		Basis:       basis,
	})
}

func missingWhich(hasStco, hasStsc bool) string {
	switch {
	case !hasStco && !hasStsc:
		return "stco+stsc"
	case !hasStco:
		return "stco"
	default:
		return "stsc"
	}
}

func fmtOffset(off int64) string {
	return "chunk offset " + itoa(off) + " outside input"
}

func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
