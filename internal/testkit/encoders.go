// Package testkit generates deterministic, self-contained bitstream
// fixtures: no external media files are referenced. Every generator is
// seeded, so the same seed reproduces byte-identical inputs and outputs.
package testkit

import (
	"archive-recovery/internal/adapter"
)

// Scenario names supported by the corpus generator.
const (
	ScenarioNormal    = "normal"    // clean units, timestamp defects only
	ScenarioTruncated = "truncated" // one dangling coding unit at the tail
	ScenarioBadIndex  = "badindex"  // legacy index with a corrupted entry
	ScenarioWrapPTS   = "wrapts"    // missing/duplicate/regress/33-bit wrap
	ScenarioBoundary  = "boundary"  // keyframe adjacent to a chunk boundary
)

// encNAL wraps an Annex B NAL unit (3-byte start code).
func encNAL(nalType byte, payload []byte) []byte {
	out := []byte{0, 0, 1, nalType}
	return append(out, payload...)
}

// frameBody produces a deterministic payload of the requested size.
func frameBody(i, size int, fill byte) []byte {
	body := make([]byte, size)
	for j := range body {
		body[j] = fill ^ byte(i) ^ byte(j)
	}
	return body
}

// encH264Stream builds an Annex B H.264 stream of n frames with an IDR
// every keyEvery frames, plus SPS/PPS metadata up front.
func encH264Stream(n, keyEvery, payloadSize int) []byte {
	var out []byte
	out = append(out, encNAL(0x67, []byte{0x10, 0x0a})...) // SPS
	out = append(out, encNAL(0x68, []byte{0xee})...)       // PPS
	for i := 0; i < n; i++ {
		if i%keyEvery == 0 {
			out = append(out, encNAL(0x65, frameBody(i, payloadSize, 0xaa))...) // IDR
		} else {
			out = append(out, encNAL(0x41, frameBody(i, payloadSize, 0xbb))...) // non-IDR slice
		}
	}
	return out
}

// encH265Stream builds an Annex B H.265 stream of n frames, IDR every
// keyEvery frames, preceded by VPS/SPS/PPS.
func encH265Stream(n, keyEvery, payloadSize int) []byte {
	var out []byte
	out = append(out, encNAL(0x40, []byte{0x01})...) // VPS (type 32)
	out = append(out, encNAL(0x42, []byte{0x02})...) // SPS (type 33)
	out = append(out, encNAL(0x44, []byte{0x03})...) // PPS (type 34)
	for i := 0; i < n; i++ {
		body := frameBody(i, payloadSize, 0xcc)
		if i%keyEvery == 0 {
			out = append(out, encNAL(0x26, body)...) // IDR_W_RADL (type 19)
		} else {
			out = append(out, encNAL(0x02, body)...) // TRAIL_R (type 1)
		}
	}
	return out
}

func encOBU(obuType int, payload []byte) []byte {
	h := byte(obuType)<<3 | 0x02 // has_size_field
	out := []byte{h}
	v := len(payload)
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
		} else {
			out = append(out, b)
			break
		}
	}
	return append(out, payload...)
}

// encAV1Stream builds a low-overhead AV1 stream of n frames; every
// keyEvery-th frame is a KEY_FRAME, others are INTER frames.
func encAV1Stream(n, keyEvery, payloadSize int) []byte {
	var out []byte
	out = append(out, encOBU(adapter.OBUTypeSeqHeaderForTest(), []byte{0x01, 0x42})...)
	for i := 0; i < n; i++ {
		first := byte(0x40) // show_existing=0, frame_type=INTER
		if i%keyEvery == 0 {
			first = 0x00 // KEY_FRAME
		}
		body := append([]byte{first, byte(i)}, frameBody(i, payloadSize, 0x77)...)
		out = append(out, encOBU(adapter.OBUTypeFrameForTest(), body)...)
	}
	return out
}

// appendTruncation appends a codec-specific dangling unit (header only).
func appendTruncation(codec string, stream []byte) []byte {
	switch codec {
	case "h264":
		return append(stream, 0, 0, 1, 0x65)
	case "h265":
		return append(stream, 0, 0, 1, 0x26)
	default: // av1: frame OBU header claiming 40 payload bytes, only 3 present
		return append(stream, byte(adapter.OBUTypeFrameForTest())<<3|0x02, 40, 1, 2, 3)
	}
}

// insertAnnexBPadding prepends legal zero-byte padding before the stream's
// first start code to shift later boundaries.
func insertAnnexBPadding(stream []byte, n int) []byte {
	pad := make([]byte, n)
	return append(pad, stream...)
}

// insertAV1Padding inserts an AV1 padding OBU of n bytes.
func insertAV1Padding(n int) []byte {
	return encOBU(15, make([]byte, n))
}
