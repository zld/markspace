package bitstream

import (
	"bytes"

	mp4 "github.com/abema/go-mp4"
)

// Issue describes a structural problem found during a scan.
type Issue struct {
	Offset int64
	Reason string
}

// ScanResult is the structural index of one Annex B buffer.
type ScanResult struct {
	NALs      []NAL
	Keyframes []int // indices into NALs with Type == NALTypeIDR
	Issues    []Issue
	// RecoverableBoundary is the offset just past the last complete NAL:
	// everything before it is sound even when the tail is truncated.
	RecoverableBoundary int64
}

// IsMP4 reports whether data looks like an ISO BMFF (MP4) container
// rather than an Annex B elementary stream.
func IsMP4(data []byte) bool {
	_, err := mp4.Probe(bytes.NewReader(data))
	return err == nil
}

// Scan walks an Annex B buffer, locating every NAL unit, flagging IDR
// keyframes, and reporting structural issues. base is added to all
// reported offsets so results can be expressed in input-stream bytes.
// tailComplete tells the scanner whether the buffer ends cleanly: when
// false, the final NAL (which has no following start code) is reported
// as truncated.
func Scan(data []byte, base int64, tailComplete bool) *ScanResult {
	res := &ScanResult{}
	pos := 0
	for pos < len(data) {
		idx, size := FindStartCode(data, pos)
		if idx < 0 {
			break
		}
		hdr := idx + size
		if hdr >= len(data) {
			res.Issues = append(res.Issues, Issue{Offset: base + int64(idx),
				Reason: "start code without NAL header"})
			break
		}
		next, _ := FindStartCode(data, hdr)
		end := len(data)
		truncated := !tailComplete
		if next >= 0 {
			end = next
			truncated = false
		}
		if end == hdr {
			res.Issues = append(res.Issues, Issue{Offset: base + int64(idx),
				Reason: "empty NAL unit (invalid length)"})
			pos = hdr
			continue
		}
		nalType, refIDC, forbidden := ParseHeader(data[hdr])
		if forbidden != 0 {
			res.Issues = append(res.Issues, Issue{Offset: base + int64(hdr),
				Reason: "forbidden_zero_bit set"})
		}
		res.NALs = append(res.NALs, NAL{
			Offset:    base + int64(idx),
			HeaderAt:  base + int64(hdr),
			Type:      nalType,
			RefIDC:    refIDC,
			Length:    int64(end - idx),
			Truncated: truncated,
		})
		if nalType == NALTypeIDR {
			res.Keyframes = append(res.Keyframes, len(res.NALs)-1)
		}
		pos = end
		if truncated {
			break
		}
	}
	if n := len(res.NALs); n > 0 {
		last := res.NALs[n-1]
		if last.Truncated {
			res.RecoverableBoundary = last.Offset
			res.Issues = append(res.Issues, Issue{Offset: last.Offset,
				Reason: "truncated NAL at end of input"})
		} else {
			res.RecoverableBoundary = last.Offset + last.Length
		}
	}
	return res
}
