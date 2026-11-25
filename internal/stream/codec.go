// Package stream performs bounded, streaming scans of H.264/H.265/AV1
// Annex B elementary streams. It locates start codes, validates NAL/OBU
// headers, groups NALs into access units and enforces a hard single-AU
// size cap so memory stays bounded regardless of input length.
package stream

import (
	"fmt"

	"mediad/internal/errors"
)

// Codec identifies the elementary stream syntax.
type Codec string

const (
	H264 Codec = "h264"
	HEVC Codec = "hevc"
	AV1  Codec = "av1"
)

// ParseCodec accepts the CLI spelling used by the demo command.
func ParseCodec(s string) (Codec, error) {
	switch Codec(s) {
	case H264, "h265", HEVC:
		if s == "h265" {
			return HEVC, nil
		}
		return Codec(s), nil
	case AV1:
		return AV1, nil
	default:
		return "", fmt.Errorf("%w: codec %q", errBadCodec, s)
	}
}

var errBadCodec = errs.New(errs.Fatal, errs.CodeUnsupportedHeader, "unsupported codec")

// NALClass groups NAL/OBU types by their role in access-unit framing.
type NALClass int

const (
	NALOther    NALClass = iota // non-VCL miscellany
	NALVCL                      // coded slice / frame data
	NALAUD                      // access-unit / temporal delimiter
	NALParamSet                 // SPS/PPS/VPS/sequence header
	NALSEI                      // supplemental information
)

// NALInfo is the result of validating one NAL unit / OBU header.
type NALInfo struct {
	Type       uint8
	Class      NALClass
	Keyframe   bool // IDR slice, or AV1 sequence header
	MinPayload int  // minimum payload bytes past the syntax header
}

// AV1 OBU type constants (AV1 spec §6.2.2).
const (
	OBUSequenceHeader = 1
	OBUTemporalDelim  = 2
	OBUFrameHeader    = 3
	OBUFrame          = 6
	OBUMetadata       = 7
)

// InspectNAL validates the header bytes of one start-code-framed unit.
// header must contain at least the first syntax byte; payloadLen is the
// total unit length excluding the start code. The returned error, if any,
// carries the error category; the caller anchors it at the unit offset.
func (c Codec) InspectNAL(payload []byte) (NALInfo, error) {
	if len(payload) == 0 {
		return NALInfo{}, errs.New(errs.Recoverable, errs.CodeIncompleteHeader,
			"empty NAL unit: start code with no header byte")
	}
	switch c {
	case H264:
		return inspectH264(payload)
	case HEVC:
		return inspectHEVC(payload)
	case AV1:
		return inspectAV1(payload)
	default:
		return NALInfo{}, errs.New(errs.Fatal, errs.CodeUnsupportedHeader, "codec %q", c)
	}
}

func inspectH264(p []byte) (NALInfo, error) {
	b0 := p[0]
	if b0&0x80 != 0 {
		return NALInfo{}, errs.New(errs.Recoverable, errs.CodeUnsupportedHeader,
			"H.264 forbidden_zero_bit is set")
	}
	typ := b0 & 0x1f
	if typ == 0 || typ >= 24 {
		return NALInfo{}, errs.New(errs.Recoverable, errs.CodeUnsupportedHeader,
			"H.264 reserved nal_unit_type %d", typ)
	}
	info := NALInfo{Type: typ, MinPayload: 1}
	switch {
	case typ == 5:
		info.Class, info.Keyframe, info.MinPayload = NALVCL, true, 2
	case typ == 1 || (typ >= 2 && typ <= 5):
		info.Class, info.MinPayload = NALVCL, 2
	case typ == 7 || typ == 8 || typ == 13:
		info.Class, info.MinPayload = NALParamSet, 2
	case typ == 9:
		info.Class, info.MinPayload = NALAUD, 1
	case typ == 6:
		info.Class, info.MinPayload = NALSEI, 2
	}
	return info, nil
}

func inspectHEVC(p []byte) (NALInfo, error) {
	if len(p) < 2 {
		return NALInfo{}, errs.New(errs.Recoverable, errs.CodeIncompleteHeader,
			"H.265 NAL header needs 2 bytes, got %d", len(p))
	}
	b0 := p[0]
	if b0&0x80 != 0 {
		return NALInfo{}, errs.New(errs.Recoverable, errs.CodeUnsupportedHeader,
			"H.265 forbidden_zero_bit is set")
	}
	typ := (b0 >> 1) & 0x3f
	if typ == 0 || (typ >= 11 && typ <= 16) || (typ >= 23 && typ <= 31) || typ > 40 {
		return NALInfo{}, errs.New(errs.Recoverable, errs.CodeUnsupportedHeader,
			"H.265 reserved nal_unit_type %d", typ)
	}
	info := NALInfo{Type: typ, MinPayload: 2}
	switch {
	case (typ >= 1 && typ <= 10) || (typ >= 17 && typ <= 22):
		info.Class = NALVCL
		info.Keyframe = typ == 19 || typ == 20 || typ == 21
		info.MinPayload = 3
	case typ == 32 || typ == 33 || typ == 34:
		info.Class, info.MinPayload = NALParamSet, 3
	case typ == 35:
		info.Class, info.MinPayload = NALAUD, 2
	case typ == 39 || typ == 40:
		info.Class, info.MinPayload = NALSEI, 3
	}
	return info, nil
}

func inspectAV1(p []byte) (NALInfo, error) {
	b0 := p[0]
	if b0&0x80 != 0 {
		return NALInfo{}, errs.New(errs.Recoverable, errs.CodeUnsupportedHeader,
			"AV1 obu_forbidden_bit is set")
	}
	if b0&0x04 != 0 {
		return NALInfo{}, errs.New(errs.Recoverable, errs.CodeUnsupportedHeader,
			"AV1 extension OBUs are not supported")
	}
	typ := (b0 >> 3) & 0x0f
	if typ == 0 || typ > 8 {
		return NALInfo{}, errs.New(errs.Recoverable, errs.CodeUnsupportedHeader,
			"AV1 reserved obu_type %d", typ)
	}
	info := NALInfo{Type: typ}
	switch typ {
	case OBUSequenceHeader:
		info.Class, info.Keyframe, info.MinPayload = NALParamSet, true, 1
	case OBUTemporalDelim:
		info.Class = NALAUD // zero-length payload is legal here
	case OBUFrame:
		info.Class, info.MinPayload = NALVCL, 1
	default:
		info.MinPayload = 1
	}
	return info, nil
}
