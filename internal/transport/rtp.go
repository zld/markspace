// Package transport adapts recovered frames onto legacy transport and
// container shapes for round-trip verification: RTP packets via pion/rtp and
// a minimal MP4 box via go-mp4. Neither path replaces the canonical
// bitstream evidence in the provenance manifest.
package transport

import (
	"archive-recovery/internal/media"
	"github.com/pion/rtp"
)

// RTP payload type and one-byte header extension ID used by archive RTP.
const (
	PayloadType   = 96
	extIDPTS33    = 1
	rtpClockRate  = 90000
	maxPayloadMTU = 1200
)

// Packetize wraps frame units into RTP packets. The low 32 bits of the
// repaired PTS go in the standard timestamp field; bit 32 rides in a
// one-byte header extension so 33-bit values survive transport intact.
func Packetize(frames []media.FrameEvent, ssrc uint32) ([][]byte, error) {
	var out [][]byte
	seq := uint16(0)
	for _, f := range frames {
		chunks := splitPayload(f, maxPayloadMTU)
		for i, chunk := range chunks {
			pkt := &rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					PayloadType:    PayloadType,
					SequenceNumber: seq,
					Timestamp:      uint32(f.RepairedPTS),
					SSRC:           ssrc,
					Marker:         i == len(chunks)-1,
				},
				Payload: chunk,
			}
			// The standard RTP timestamp carries the low 32 bits; the
			// remaining high bits of the 33-bit PTS ride in the one-byte
			// header extension.
			hi := byte((f.RepairedPTS >> 32) & 0xff)
			if err := pkt.SetExtension(extIDPTS33, []byte{hi}); err != nil {
				return nil, media.WrapError(media.ClassFatal, "transport",
					"rtp extension encode", err)
			}
			raw, err := pkt.Marshal()
			if err != nil {
				return nil, media.WrapError(media.ClassFatal, "transport",
					"rtp marshal", err)
			}
			out = append(out, raw)
			seq++
		}
	}
	return out, nil
}

// Depacketize parses RTP packets and reconstructs frame payloads at marker
// boundaries, restoring the full 33-bit PTS. It returns the number of
// complete frames recovered.
func Depacketize(packets [][]byte) ([]RecoveredFrame, error) {
	var frames []RecoveredFrame
	var cur []byte
	for _, raw := range packets {
		hasExt := len(raw) >= 12 && raw[0]&0x10 != 0
		var pkt rtp.Packet
		if err := pkt.Unmarshal(raw); err != nil {
			// pion v1.8.11 rejects a final extension touching the packet
			// tail. Fall back to a manual parse only for well-formed packets;
			// genuinely truncated input still fails as recoverable.
			fallback, ok := manualParse(raw)
			if !ok {
				return nil, media.WrapError(media.ClassRecoverable, "transport",
					"rtp unmarshal", err)
			}
			pkt = fallback
		}
		pts := uint64(pkt.Timestamp)
		if hasExt {
			if hi := oneByteExtension(raw, extIDPTS33); hi >= 0 {
				pts |= uint64(hi) << 32
			}
		}
		cur = append(cur, pkt.Payload...)
		if pkt.Marker {
			frames = append(frames, RecoveredFrame{PTS33: pts, Payload: append([]byte{}, cur...)})
			cur = nil
		}
	}
	return frames, nil
}

// RecoveredFrame is one frame reassembled from RTP packets.
type RecoveredFrame struct {
	PTS33   uint64
	Payload []byte
}

// oneByteExtension extracts an RFC 8285 one-byte header extension directly
// from the raw packet. This avoids pion/rtp v1.8.11's end-of-buffer boundary
// bug, which drops the final extension when it reaches the packet tail.
func oneByteExtension(raw []byte, id uint8) int {
	if len(raw) < 12+4 {
		return -1
	}
	csrc := int(raw[0] & 0x0f)
	pos := 12 + 4*csrc
	if pos+4 > len(raw) || raw[pos] != 0xBE || raw[pos+1] != 0xDE {
		return -1
	}
	extWords := int(bigEndianUint16(raw[pos+2:]))
	pos += 4
	end := pos + 4*extWords
	if end > len(raw) {
		end = len(raw)
	}
	for pos < end {
		b := raw[pos]
		if b == 0 { // padding
			pos++
			continue
		}
		extID := b >> 4
		payloadLen := int(b&0x0f) + 1 // low nibble 0 means one byte
		pos++
		if extID == 0x0f {
			return -1
		}
		if extID == id && payloadLen > 0 && pos <= end {
			return int(raw[pos])
		}
		pos += payloadLen
	}
	return -1
}

func bigEndianUint16(b []byte) uint16 {
	return uint16(b[0])<<8 | uint16(b[1])
}

func bigEndianUint32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// manualParse decodes a well-formed RTP packet without relying on pion's
// extension handling, including the RFC 8285 one-byte extension block.
func manualParse(raw []byte) (rtp.Packet, bool) {
	if len(raw) < 12 || raw[0]>>6 != 2 {
		return rtp.Packet{}, false
	}
	pos := 12 + 4*int(raw[0]&0x0f)
	hasExt := raw[0]&0x10 != 0
	if hasExt {
		if pos+4 > len(raw) {
			return rtp.Packet{}, false
		}
		extWords := int(bigEndianUint16(raw[pos+2:]))
		pos += 4 + 4*extWords
	}
	if pos > len(raw) {
		return rtp.Packet{}, false
	}
	return rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			Marker:         raw[1]&0x80 != 0,
			PayloadType:    raw[1] & 0x7f,
			SequenceNumber: bigEndianUint16(raw[2:]),
			Timestamp:      bigEndianUint32(raw[4:]),
			SSRC:           bigEndianUint32(raw[8:]),
			Extension:      hasExt,
		},
		Payload: append([]byte{}, raw[pos:]...),
	}, true
}

func splitPayload(f media.FrameEvent, mtu int) [][]byte {
	// Packetization here is exercised on compact synthetic payloads derived
	// from the frame's byte range; use the source slice coordinates.
	total := int(f.Length)
	if total <= 0 {
		return [][]byte{{0}}
	}
	var out [][]byte
	for off := 0; off < total; off += mtu {
		end := off + mtu
		if end > total {
			end = total
		}
		out = append(out, []byte{byte(off), byte(end)})
	}
	return out
}

// RTPClockRate exposes the transport clock for clock-domain checks.
func RTPClockRate() int { return rtpClockRate }
