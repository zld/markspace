// Package stream scans packetized H.264/H.265 video into per-sample
// structures on the unified media clock. Every parsing failure is
// localized to packet seqs, byte offsets and media time instead of
// aborting the analysis, and subsequent parseable samples keep flowing.
package stream

import (
	"errors"

	"mediacheck/internal/fault"
)

// Codec identifies the video codec of a packetized stream.
type Codec string

const (
	// CodecH264 is RFC 6184 AVC packetization.
	CodecH264 Codec = "H264"
	// CodecH265 is RFC 7798 HEVC packetization.
	CodecH265 Codec = "H265"
)

// nalKind classifies a NAL unit after the codec-specific header bits.
type nalKind int

const (
	nalVCL  nalKind = iota // slice layer
	nalIDR                 // random-access slice (key frame)
	nalSPS                 // sequence parameter set
	nalPPS                 // picture parameter set
	nalOther               // parameter/non-VCL unit we still record
)

// ErrEmptyPayload means an RTP packet carried no payload bytes at all,
// which is an illegal-length style fault rather than a panic.
var ErrEmptyPayload = errors.New("empty RTP payload")

// nalHeader is the codec-independent view of a NAL unit header.
type nalHeader struct {
	Type uint32
	Kind nalKind
}

func parseNALHeader(codec Codec, b byte) nalHeader {
	if codec == CodecH264 {
		t := uint32(b & 0x1F)
		switch {
		case t == 7:
			return nalHeader{Type: t, Kind: nalSPS}
		case t == 8:
			return nalHeader{Type: t, Kind: nalPPS}
		case t == 5:
			return nalHeader{Type: t, Kind: nalIDR}
		case t >= 1 && t <= 23:
			return nalHeader{Type: t, Kind: nalVCL}
		default:
			return nalHeader{Type: t, Kind: nalOther}
		}
	}
	// HEVC carries the 6-bit NAL type in bits 1..6; bit 0 is
	// forbidden_zero in NAL headers and the FU flag byte.
	t := uint32((b & 0x7E) >> 1)
	switch {
	case t == 33 || t == 34: // SPS / PPS
		if t == 33 {
			return nalHeader{Type: t, Kind: nalSPS}
		}
		return nalHeader{Type: t, Kind: nalPPS}
	case t >= 16 && t <= 21: // CRA, BLA, IDR slices
		return nalHeader{Type: t, Kind: nalIDR}
	case t <= 31:
		return nalHeader{Type: t, Kind: nalVCL}
	default:
		return nalHeader{Type: t, Kind: nalOther}
	}
}

// fuHeader is the RFC 6184 FU indicator/header pair (H.264) or the
// RFC 7798 FU header (H.265); both expose Start/End flags and NAL type.
type fuHeader struct {
	Start bool
	End   bool
	Type  uint32
}

func parseFU(codec Codec, payload []byte) (fuHeader, error) {
	minLen := 2
	if codec == CodecH265 {
		minLen = 3 // two-byte payload header plus the FU byte
	}
	if len(payload) < minLen {
		return fuHeader{}, ErrEmptyPayload
	}
	if codec == CodecH264 {
		return fuHeader{
			Start: payload[1]&0x80 != 0,
			End:   payload[1]&0x40 != 0,
			Type:  uint32(payload[1] & 0x1F),
		}, nil
	}
	// RFC 7798 FU header byte: S(bit7) E(bit6), FuType in the low six
	// bits (same type numbering as the NAL unit header).
	return fuHeader{
		Start: payload[2]&0x80 != 0,
		End:   payload[2]&0x40 != 0,
		Type:  uint32(payload[2] & 0x3F),
	}, nil
}

// fuIndicatorByte reconstructs the single-byte NAL header of a
// fragmented unit from its original NAL type.
func fuIndicatorByte(codec Codec, nalType uint32, original byte) byte {
	if codec == CodecH264 {
		return (original & 0xE0) | byte(nalType&0x1F)
	}
	return (original & 0x81) | byte((nalType&0x3F)<<1)
}

// isFragmented / isAggregate recognize the packetization modes.
func isFragmented(codec Codec, payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	if codec == CodecH264 {
		t := payload[0] & 0x1F
		return t == 28 || t == 29
	}
	t := (payload[0] & 0x7E) >> 1
	return t == 49
}

func isAggregate(codec Codec, payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	if codec == CodecH264 {
		return payload[0]&0x1F == 24
	}
	t := (payload[0] & 0x7E) >> 1
	return t == 48
}

// unknownNAL reports a NAL type the scanner does not recognize. The
// caller supplies the location context so the fault stays localized.
func unknownNALFault(codec Codec, nalType uint32, loc location) fault.Fault {
	return fault.Fault{
		Class:   fault.ClassUnknownNAL,
		Codec:   string(codec),
		NALType: nalType,
		Seq:     loc.seq,
		Byte:    loc.byte,
		Time:    loc.time,
		Detail:  "NAL type outside known H.264/H.265 ranges",
	}
}
