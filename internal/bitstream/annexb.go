package bitstream

import "mediacore/internal/fault"

// nalUnit is one Annex B network abstraction layer unit.
type nalUnit struct {
	startCode int64 // offset of the first byte of the start code
	offset    int64 // offset of the NAL header, after the start code
	payload   []byte
}

// splitAnnexB locates start codes. Data that does not begin with a start code
// or a run of zeroes ending in one is an illegal boundary, as is trailing data
// that cannot form a start code.
func splitAnnexB(codec Codec, data []byte) ([]nalUnit, error) {
	var units []nalUnit
	i := 0
	seenStart := false
	for i < len(data) {
		if data[i] != 0 {
			if !seenStart {
				return nil, badBoundary(codec, int64(i), "stream does not begin with an Annex B start code")
			}
			return nil, badBoundary(codec, int64(i), "data outside a NAL unit start code")
		}
		// A start code is 00 00 01 or 00 00 00 01. Leading zeroes are allowed.
		j := i
		for j < len(data) && data[j] == 0 {
			j++
		}
		if j >= len(data) {
			return nil, truncated(codec, int64(i), "start code")
		}
		if data[j] != 1 {
			return nil, badBoundary(codec, int64(j), "expected start code prefix 0x000001")
		}
		start := j + 1
		startCode := i
		seenStart = true
		// Find the next start code to bound this unit.
		end := len(data)
		k := start
		for k+2 < len(data) {
			if data[k] == 0 && data[k+1] == 0 && data[k+2] == 1 {
				end = k
				// Trim a trailing zero that belongs to a four byte start code.
				for end > start && data[end-1] == 0 {
					end--
				}
				break
			}
			k++
		}
		if end <= start {
			return nil, truncated(codec, int64(start), "NAL unit")
		}
		units = append(units, nalUnit{startCode: int64(startCode), offset: int64(start), payload: data[start:end]})
		i = end
	}
	if len(units) == 0 {
		return nil, fault.New(fault.CodeScanNoFrames, 0, string(codec), "no NAL units found")
	}
	return units, nil
}
