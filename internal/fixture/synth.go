package fixture

import (
	"math/rand"

	"mediaroute/internal/codec"
	"mediaroute/internal/transport"
)

// H.264/H.265 synthetic NAL header bytes (types see codec package).
const (
	h264SPSHeader = 0x67 // type 7
	h264IDRHeader = 0x65 // type 5
	h264PHeader   = 0x41 // type 1 non-IDR slice
	h265IDRHeader = byte(19 << 1)
	h265PHeader   = byte(1 << 1) // TRAIL_R, type 1
)

// synthFrame builds a deterministic bare-stream frame. Keyframes contain a
// key NAL unit; NAL frames end with a trailing start code so the codec
// parser marks every unit complete.
func synthFrame(c codec.Codec, sample uint32, keyframe, complete bool) []byte {
	payload := synthPayload(c, sample)
	switch c {
	case codec.H264:
		if keyframe {
			return appendNAL(appendNAL(nil, h264SPSHeader, payload),
				h264IDRHeader, payload)
		}
		return appendNAL(nil, h264PHeader, payload)
	case codec.H265:
		hdr := h265PHeader
		if keyframe {
			hdr = h265IDRHeader
		}
		return appendNAL(nil, hdr, payload)
	case codec.AV1:
		return synthAV1Frame(keyframe, payload)
	default:
		// Unknown tags are intentionally not synthesized; callers validate.
		return payload
	}
}

func synthPayload(c codec.Codec, sample uint32) []byte {
	// Deterministic, distinctive content derived from sample number only.
	return []byte{
		byte(sample),
		byte(sample >> 8),
		byte(c[0]),
		0xA5,
		0x5A,
		byte(sample) ^ 0xFF,
	}
}

// appendNAL adds [00 00 00 01][header][payload] and a trailing start code
// so the frame is parseable as complete Annex B units.
func appendNAL(dst []byte, header byte, payload []byte) []byte {
	dst = append(dst, 0, 0, 0, 1, header)
	dst = append(dst, payload...)
	dst = append(dst, 0, 0, 1) // completeness terminator
	return dst
}

// synthAV1Frame builds a length-delimited OBU sequence: frame header OBU
// (keyframe bit in first payload byte) plus a tile group OBU.
func synthAV1Frame(keyframe bool, payload []byte) []byte {
	hdrPayload := []byte{0x00}
	if keyframe {
		hdrPayload[0] = 0x80
	}
	out := appendOBU(nil, codec.OBUFrameHeader, hdrPayload)
	out = appendOBU(out, codec.OBUTileGroup, payload)
	return out
}

func appendOBU(dst []byte, typ byte, payload []byte) []byte {
	dst = append(dst, (typ<<3)|0x02) // has_size=1
	dst = append(dst, byte(len(payload)))
	return append(dst, payload...)
}

// injectDuplicates re-sends existing packets at later positions. Each
// duplicate keeps the original SSRC/seq so the reorderer counts it once.
func injectDuplicates(rng *rand.Rand, fx *Fixture, n int) {
	if n <= 0 || len(fx.Packets) == 0 {
		return
	}
	for i := 0; i < n; i++ {
		idx := rng.Intn(len(fx.Packets))
		src := fx.Packets[idx]
		dup := &transport.Packet{
			SSRC: src.SSRC, Seq: src.Seq, Timestamp: src.Timestamp,
			Marker: src.Marker, Payload: append([]byte(nil), src.Payload...),
		}
		// Insert within the reorder window of a later position.
		at := idx + 1
		if at >= len(fx.Packets) {
			at = len(fx.Packets)
		}
		fx.Packets = insertAt(fx.Packets, at, dup)
	}
}

// injectReorders swaps neighboring packets belonging to the same SSRC while
// staying inside the reorder window, so the reorderer repairs them.
func injectReorders(rng *rand.Rand, fx *Fixture, n int) {
	if n <= 0 || len(fx.Packets) < 4 {
		return
	}
	done := 0
	for tries := 0; tries < n*10 && done < n; tries++ {
		i := rng.Intn(len(fx.Packets) - 2)
		a, b := fx.Packets[i], fx.Packets[i+1]
		// Only swap same-SSRC neighbors that are currently in sequence.
		if a.SSRC != b.SSRC || int16(b.Seq-a.Seq) != 1 {
			continue
		}
		fx.Packets[i], fx.Packets[i+1] = b, a
		done++
	}
}

func insertAt(pkts []*transport.Packet, at int, p *transport.Packet) []*transport.Packet {
	pkts = append(pkts, nil)
	copy(pkts[at+1:], pkts[at:])
	pkts[at] = p
	return pkts
}
