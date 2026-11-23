// Package ingest scans raw H.264/H.265/AV1 bitstreams, validates keyframe
// indexes, and parses color/audio metadata. It never fabricates time or
// metadata: faults are reported as classified media.Error values.
package ingest

import "mediacore/media"

// Codec selects the elementary stream syntax.
type Codec int

const (
	CodecH264 Codec = iota // Annex B start-code delimited
	CodecH265              // Annex B start-code delimited
	CodecAV1               // low-overhead OBU stream
)

func (c Codec) String() string {
	switch c {
	case CodecH264:
		return "h264"
	case CodecH265:
		return "h265"
	case CodecAV1:
		return "av1"
	}
	return "unknown"
}

// Hard bounds: memory must not grow with input duration, so any single
// unit or index entry beyond these limits is rejected, not buffered.
const (
	MaxUnitSize  = 1 << 20 // 1 MiB per NAL/OBU
	MaxUnitCount = 1 << 16 // per scan window
)

// Unit is one delimited NAL unit (H.264/H.265) or OBU (AV1).
type Unit struct {
	Offset   int64 // byte offset of payload (after delimiter) in the stream
	Size     int64 // payload size in bytes
	Type     uint8 // codec-specific unit type
	Keyframe bool
}

// Scan delimits units and flags keyframes. A corrupt unit (bad header,
// zero length, truncated tail) returns CodeNALCorrupt located at the
// unit's offset; oversized input returns CodeLimitExceeded.
func Scan(data []byte, codec Codec) ([]Unit, error) {
	if codec == CodecAV1 {
		return scanOBU(data)
	}
	return scanAnnexB(data, codec)
}

func scanAnnexB(data []byte, codec Codec) ([]Unit, error) {
	var units []Unit
	i := 0
	for i < len(data) {
		scLen := startCodeLen(data[i:])
		if scLen == 0 {
			return nil, media.Fault(media.CodeNALCorrupt, "ingest.Scan", int64(i),
				"missing start code for %s", codec)
		}
		start := i + scLen
		// find next start code or end of data
		end := len(data)
		for j := start; j+2 < len(data); j++ {
			if data[j] == 0 && data[j+1] == 0 && (data[j+2] == 1 || (j+3 < len(data) && data[j+2] == 0 && data[j+3] == 1)) {
				end = j
				break
			}
		}
		payload := data[start:end]
		if len(payload) == 0 {
			return nil, media.Fault(media.CodeNALCorrupt, "ingest.Scan", int64(start),
				"zero-length unit")
		}
		if len(payload) > MaxUnitSize {
			return nil, media.Fault(media.CodeLimitExceeded, "ingest.Scan", int64(start),
				"unit size %d exceeds %d", len(payload), MaxUnitSize)
		}
		u, err := classifyAnnexB(payload, codec, int64(start))
		if err != nil {
			return nil, err
		}
		u.Size = int64(len(payload))
		units = append(units, u)
		if len(units) > MaxUnitCount {
			return nil, media.Fault(media.CodeLimitExceeded, "ingest.Scan", int64(start),
				"unit count exceeds %d", MaxUnitCount)
		}
		i = end
	}
	return units, nil
}

func startCodeLen(b []byte) int {
	if len(b) >= 3 && b[0] == 0 && b[1] == 0 && b[2] == 1 {
		return 3
	}
	if len(b) >= 4 && b[0] == 0 && b[1] == 0 && b[2] == 0 && b[3] == 1 {
		return 4
	}
	return 0
}

func classifyAnnexB(p []byte, codec Codec, off int64) (Unit, error) {
	u := Unit{Offset: off}
	switch codec {
	case CodecH264:
		if p[0]&0x80 != 0 {
			return u, media.Fault(media.CodeNALCorrupt, "ingest.Scan", off,
				"h264 forbidden_zero_bit set")
		}
		u.Type = p[0] & 0x1F
		u.Keyframe = u.Type == 5 // IDR
	case CodecH265:
		if len(p) < 2 {
			return u, media.Fault(media.CodeNALCorrupt, "ingest.Scan", off,
				"h265 header truncated")
		}
		if p[0]&0x80 != 0 {
			return u, media.Fault(media.CodeNALCorrupt, "ingest.Scan", off,
				"h265 forbidden_zero_bit set")
		}
		u.Type = (p[0] >> 1) & 0x3F
		u.Keyframe = u.Type == 19 || u.Type == 20 // IDR_W_RADL / IDR_N_LP
	}
	return u, nil
}

// scanOBU parses the AV1 low-overhead OBU format: 1-byte header with
// has_size_field, followed by a LEB128 size and the payload.
func scanOBU(data []byte) ([]Unit, error) {
	var units []Unit
	i := 0
	for i < len(data) {
		hdr := data[i]
		if hdr&0x80 != 0 {
			return nil, media.Fault(media.CodeNALCorrupt, "ingest.Scan", int64(i),
				"av1 obu_forbidden_bit set")
		}
		obuType := (hdr >> 3) & 0x0F
		hasSize := hdr&0x02 != 0
		if !hasSize {
			return nil, media.Fault(media.CodeNALCorrupt, "ingest.Scan", int64(i),
				"av1 obu without size field is not delimitable")
		}
		size, n := leb128(data[i+1:])
		if n == 0 {
			return nil, media.Fault(media.CodeNALCorrupt, "ingest.Scan", int64(i),
				"av1 truncated leb128 size")
		}
		if size > MaxUnitSize {
			return nil, media.Fault(media.CodeLimitExceeded, "ingest.Scan", int64(i),
				"obu size %d exceeds %d", size, MaxUnitSize)
		}
		start := i + 1 + n
		if start+size > len(data) {
			return nil, media.Fault(media.CodeNALCorrupt, "ingest.Scan", int64(i),
				"av1 obu truncated: need %d bytes, have %d", size, len(data)-start)
		}
		u := Unit{Offset: int64(start), Size: int64(size), Type: obuType}
		// Frame header OBU (3) or Frame OBU (6): first payload bit is
		// show_existing_frame; if 0, next 2 bits are frame_type (0=key).
		if (obuType == 3 || obuType == 6) && size > 0 {
			b := data[start]
			if b&0x80 == 0 && (b>>5)&0x3 == 0 {
				u.Keyframe = true
			}
		}
		units = append(units, u)
		if len(units) > MaxUnitCount {
			return nil, media.Fault(media.CodeLimitExceeded, "ingest.Scan", int64(i),
				"unit count exceeds %d", MaxUnitCount)
		}
		i = start + size
	}
	return units, nil
}

// leb128 decodes an unsigned LEB128 value; returns (0, 0) on truncation.
func leb128(b []byte) (int, int) {
	v := 0
	for i := 0; i < 8 && i < len(b); i++ {
		v |= int(b[i]&0x7F) << (7 * i)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return 0, 0
}
