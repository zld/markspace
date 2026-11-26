package inspect

import "encoding/binary"

// NAL is one AVC NAL unit inside an AVCC (length-prefixed) stream.
type NAL struct {
	Offset int64 // absolute file offset of the 4-byte length prefix
	Length uint32
	Type   uint8
	IDR    bool
}

// ScanAVC scans an AVCC length-prefixed NAL stream (an mdat payload).
// baseOffset is the absolute file offset of data[0]. Corruption is a
// finding, not a processing failure: if a NAL declares a length that runs
// past the end of the buffer, the returned corruptOffset is the absolute
// offset of its length prefix and scanning stops.
func ScanAVC(data []byte, baseOffset int64) (nals []NAL, corruptOffset int64) {
	corruptOffset = -1
	pos := 0
	for pos < len(data) {
		if pos+4 > len(data) {
			return nals, baseOffset + int64(pos)
		}
		n := binary.BigEndian.Uint32(data[pos:])
		if n == 0 || pos+4+int(n) > len(data) {
			return nals, baseOffset + int64(pos)
		}
		typ := data[pos+4] & 0x1f
		nals = append(nals, NAL{
			Offset: baseOffset + int64(pos),
			Length: n,
			Type:   typ,
			IDR:    typ == 5,
		})
		pos += 4 + int(n)
	}
	return nals, -1
}

// AnalyzeSegment scans one segment's mdat payload in place, setting the
// Keyframe and Corrupt findings.
func (seg *Segment) AnalyzeAVC(payload []byte) {
	nals, corrupt := ScanAVC(payload, seg.MdatOffset)
	for _, n := range nals {
		if n.IDR {
			seg.Keyframe = true
			break
		}
	}
	if corrupt >= 0 {
		seg.Corrupt = true
		seg.CorruptOffset = corrupt
	}
}

// GOPReport summarizes keyframe spacing on the unified timeline.
type GOPReport struct {
	KeyframeTimesMs []int64 `json:"keyframeTimesMs"`
	IntervalsMs     []int64 `json:"intervalsMs"`
	MaxIntervalMs   int64   `json:"maxIntervalMs"`
}

// GOP computes keyframe intervals from ordered keyframe times in ms.
func GOP(times []int64) GOPReport {
	rep := GOPReport{KeyframeTimesMs: times}
	for i := 1; i < len(times); i++ {
		iv := times[i] - times[i-1]
		rep.IntervalsMs = append(rep.IntervalsMs, iv)
		if iv > rep.MaxIntervalMs {
			rep.MaxIntervalMs = iv
		}
	}
	return rep
}

// CompareKeyframes returns the index of the first keyframe time that
// differs between two representations, or -1 if they agree.
func CompareKeyframes(a, b []int64) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}
