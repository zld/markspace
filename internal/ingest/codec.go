package ingest

import "errors"

// Opus frame durations selectable via the TOC config index, indexed by
// config 0..31 per RFC 6716 Table 1. Values are milliseconds.
var opusFrameMS = [32]float64{
	10, 20, 40, 60, // 0-3   NB SILK-only
	10, 20, 40, 60, // 4-7   WB SILK-only
	10, 20, 40, 60, // 8-11  SWB SILK-only
	10, 20,         // 12-13 FB SILK-only
	2.5, 5, 10, 20, // 14-17 CELT-only NB
	2.5, 5, 10, 20, // 18-21 CELT-only WB
	2.5, 5, 10, 20, // 22-25 CELT-only SWB
	2.5, 5, 10, 20, // 26-29 CELT-only FB
	10, 20,         // 30-31 hybrid
}

// OpusDuration parses the RTP Opus TOC (RFC 7587, RFC 6716) and returns
// the packet duration in seconds without decoding audio. Multi-channel
// mappings beyond stereo are rejected by the caller, not here.
func OpusDuration(payload []byte) (float64, error) {
	if len(payload) == 0 {
		return 0, errors.New("empty opus payload")
	}
	toc := payload[0]
	config := toc >> 3
	code := toc & 0x03 // 0:1 frame, 1:2 equal, 2:2 diff, 3:arbitrary
	frame := opusFrameMS[config] / 1000.0
	switch code {
	case 0:
		return frame, nil
	case 1, 2:
		return 2 * frame, nil
	case 3:
		// Arbitrary number of frames: the frame count is in-band.
		if len(payload) < 2 {
			return 0, errors.New("truncated opus multi-frame packet")
		}
		m := payload[1]
		switch {
		case m&0x80 == 0:
			return float64(m&0x3f) * frame, nil
		case m&0x40 == 0:
			return float64(m&0x1f) * frame, nil
		default:
			return 0, errors.New("opus VBR multi-frame packets require codec parse")
		}
	}
	return 0, errors.New("unreachable opus TOC code")
}

// validateAACRTP performs minimal structural validation of an AAC-LC RTP
// payload in generic AU mode (RFC 3640). The payload must start with the
// 16-bit AU-headers-length followed by complete AU headers; the AU data
// itself is validated later by the decoder.
func validateAACRTP(payload []byte) error {
	if len(payload) < 2 {
		return errors.New("aac rtp payload shorter than AU-headers-length")
	}
	auHeaderBits := uint16(payload[0])<<8 | uint16(payload[1])
	if auHeaderBits == 0 || auHeaderBits%16 != 0 {
		return errors.New("invalid AU-headers-length in aac rtp payload")
	}
	auHeaders := int(auHeaderBits) / 8
	if len(payload) < 2+auHeaders {
		return errors.New("truncated AU headers in aac rtp payload")
	}
	// Each AU header is one 16-bit AU-index/AU-size pair; AU-size must
	// be non-zero and the declared AU data must be present.
	var totalAU int
	for i := 0; i < auHeaders; i += 2 {
		hdr := uint16(payload[2+i])<<8 | uint16(payload[3+i])
		auSize := hdr & 0x1fff
		if auSize == 0 {
			return errors.New("zero-size access unit in aac rtp payload")
		}
		totalAU += int(auSize)
	}
	if len(payload) < 2+auHeaders+totalAU {
		return errors.New("aac access unit data shorter than declared")
	}
	return nil
}
