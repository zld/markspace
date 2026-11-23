// Package codec parses synthetic H.264/H.265 NAL unit streams and AV1 OBU
// streams with explicit boundary checks. It never decodes picture content:
// the synthetic test payloads are inspected structurally only.
package codec

import (
	"mediaroute/internal/clock"
)

// Codec identifies a stream's coding format.
type Codec string

// Supported codec tags; anything else is reported as unsupported.
const (
	H264 Codec = "h264"
	H265 Codec = "h265"
	AV1  Codec = "av1"
)

// NALUnit is one Annex B network abstraction layer unit without its start
// code prefix.
type NALUnit struct {
	Header    byte   // first byte (nal_unit_header / nal_unit_header_word0)
	Payload   []byte // full unit bytes including the header
	Offset    int    // byte offset of the start code in the source
	StartLen  int    // 3 or 4 byte start code
	Complete  bool   // false for a truncated trailing unit
}

// startCode returns the length and position of the next Annex B start code at
// or after from, or -1, -1 when none exists.
func startCode(buf []byte, from int) (pos, length int) {
	for i := from; i+2 < len(buf); i++ {
		if buf[i] == 0 && buf[i+1] == 0 {
			if buf[i+2] == 1 {
				return i, 3
			}
			if i+3 < len(buf) && buf[i+2] == 0 && buf[i+3] == 1 {
				return i, 4
			}
		}
	}
	return -1, -1
}

// SplitAnnexB splits an H.264/H.265 Annex B byte stream into NAL units.
//
// Completeness contract: the transport reassembler owns frame boundaries. A
// fully reassembled frame is passed in WITH a trailing start code (the
// reassembler appends one before calling), while a frame cut off by packet
// loss ends on raw payload bytes without it. Therefore:
//   - a unit followed by another start code is Complete=true;
//   - a unit terminated only by end-of-buffer is Complete=false, meaning the
//     frame was truncated and the unit must never produce a keyframe hit.
//
// Parsing relies only on start code positions and never indexes past the
// buffer, so malformed input cannot trigger an out-of-bounds read.
func SplitAnnexB(buf []byte) []NALUnit {
	var units []NALUnit
	pos, plen := startCode(buf, 0)
	for pos >= 0 {
		next, nlen := startCode(buf, pos+plen)
		var raw []byte
		complete := true
		if next < 0 {
			raw = buf[pos+plen:]
			complete = false // no following start code: may be cut off
		} else {
			raw = buf[pos+plen : next]
		}
		if len(raw) > 0 {
			units = append(units, NALUnit{
				Header:   raw[0],
				Payload:  raw,
				Offset:   pos,
				StartLen: plen,
				Complete: complete,
			})
		}
		if next < 0 {
			break
		}
		pos, plen = next, nlen
	}
	return units
}

// H264NALType extracts the 5-bit nal_unit_type (0..31).
func H264NALType(header byte) int { return int(header & 0x1F) }

// H265NALType extracts the 6-bit nal_unit_type from the first header byte.
func H265NALType(header byte) int { return int((header >> 1) & 0x3F) }

// H.264 unit types treated as keyframe carriers.
const (
	h264IDR  = 5
	h264SPS  = 7
	h264PPS  = 8
	h265IDR  = 19
	h265IDRN = 20
	h265VPS  = 32
	h265SPS  = 33
	h265PPS  = 34
)

// IsKeyframeNAL reports whether a complete NAL unit is a keyframe slice or a
// parameter set for the given codec. Incomplete units never match.
func IsKeyframeNAL(c Codec, u NALUnit) (bool, error) {
	if !u.Complete {
		return false, nil
	}
	switch c {
	case H264:
		t := H264NALType(u.Header)
		return t == h264IDR || t == h264SPS || t == h264PPS, nil
	case H265:
		t := H265NALType(u.Header)
		return t == h265IDR || t == h265IDRN || t == h265VPS || t == h265SPS || t == h265PPS, nil
	default:
		return false, clock.E(clock.PhaseCodec, 0, -1, false,
			"unsupported codec for NAL inspection: "+string(c), nil)
	}
}

// FrameKey reports whether a reassembled frame buffer contains a keyframe
// NAL unit. Every inspected unit must be complete; a truncated frame cannot
// produce a keyframe hit.
func FrameKey(c Codec, frame []byte) (bool, error) {
	switch c {
	case H264, H265:
	default:
		return false, clock.E(clock.PhaseCodec, 0, -1, false,
			"unsupported codec for NAL inspection: "+string(c), nil)
	}
	units := SplitAnnexB(frame)
	if len(units) == 0 {
		return false, nil
	}
	any := false
	for _, u := range units {
		if !u.Complete {
			return false, nil
		}
		ok, err := IsKeyframeNAL(c, u)
		if err != nil {
			return false, err
		}
		any = any || ok
	}
	return any, nil
}
