package fixture

import (
	"encoding/binary"

	"hlsflow/internal/ts"
)

// Frame geometry. Every generated frame carries the same tick spacing so all
// timings are deterministic and independent of wall time.
const (
	framesPerSegment = 40
	ticksPerFrame    = 5495 // ~61.06ms -> ~2.44s per source segment
	idrInterval      = 40   // one IDR per segment, at its first frame
)

// encodeClock writes a 33-bit 90kHz timestamp into the 5-byte TS layout.
func encodeClock(pts int64) []byte {
	out := make([]byte, 5)
	out[0] = 0x21 | byte((pts>>30)&0x07)<<1
	out[1] = byte(pts >> 22)
	out[2] = byte(pts>>14) | 1
	out[3] = byte(pts >> 7)
	out[4] = byte(pts<<1) | 1
	return out
}

// buildPES assembles a PES packet with a PTS header and the given payload.
func buildPES(streamID byte, pts int64, payload []byte) []byte {
	header := append([]byte{0x00, 0x00, 0x01, streamID}, 0x00, 0x00)
	// PTS_DTS_flags = 10 (PTS only), header data length 5.
	header = append(header, 0x80, 0x80, 0x05)
	header = append(header, encodeClock(pts)...)
	return append(header, payload...)
}

// videoFrame returns an Annex B H.264 access unit. IDR frames are prefixed by
// SPS/PPS so a cut segment stays independently decodable. Non-IDR frames are
// small slice NALs; content is synthetic but structurally valid at the TS
// level, which is all the pipeline inspects.
func videoFrame(idr bool, frameIndex int) []byte {
	var au []byte
	nal := func(nalType byte, refIDC byte, body []byte) []byte {
		out := []byte{0, 0, 0, 1, (refIDC << 5) | nalType}
		return append(out, body...)
	}
	if idr {
		au = append(au, nal(ts.NALSPS, 3, []byte{0x4d, 0x40, 0x1f})...)
		au = append(au, nal(ts.NALPPS, 3, []byte{0x68, 0xce, 0x38, 0x80})...)
		au = append(au, nal(ts.NALIDR, 3, sliceBody(frameIndex))...)
		return au
	}
	au = append(au, nal(ts.NALNonIDR, 2, sliceBody(frameIndex))...)
	return au
}

// sliceBody produces deterministic slice payload bytes.
func sliceBody(frameIndex int) []byte {
	body := make([]byte, 24)
	binary.BigEndian.PutUint32(body, uint32(frameIndex))
	for i := 4; i < len(body); i++ {
		body[i] = byte((frameIndex*7 + i) & 0xFF)
	}
	return body
}

// audioFrame returns a stand-in AAC access unit: a 7-byte ADTS-like header
// followed by deterministic payload bytes.
func audioFrame(frameIndex int) []byte {
	frame := make([]byte, 7+16)
	// ADTS sync word and a fixed (synthetic) configuration.
	frame[0], frame[1] = 0xFF, 0xF1
	frame[2] = 0x50
	frame[3] = 0x80
	frame[4], frame[5], frame[6] = 0x1F, 0xFC, 0x00
	for i := 7; i < len(frame); i++ {
		frame[i] = byte((frameIndex*3 + i) & 0xFF)
	}
	return frame
}

// segmentTS builds one deterministic source transport stream. renditionOffset
// shifts every PTS to model per-rendition timeline drift. segmentSeq sets the
// starting PTS and the IDR placement.
func segmentTS(segmentSeq int, renditionOffset int64) []byte {
	m := NewMuxer()
	base := int64(segmentSeq*framesPerSegment*ticksPerFrame) + renditionOffset
	for f := 0; f < framesPerSegment; f++ {
		pts := base + int64(f)*ticksPerFrame
		idr := f == 0
		video := videoFrame(idr, segmentSeq*framesPerSegment+f)
		m.writePayload(videoPID, true, buildPES(0xE0, pts, video))
		// Audio shares the video timeline (AAC sample-aligned per frame here).
		m.writePayload(audioPID, true, buildPES(0xC0, pts, audioFrame(f)))
		m.maybePSI()
	}
	return m.Bytes()
}
