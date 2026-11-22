package fixture

import (
	"encoding/binary"
	"fmt"
)

// VideoAU builds one synthetic H.264-like access unit. Each unit carries an
// access delimiter, an SPS/PPS on sync frames, and a SEI block. When
// withColor is set the SEI advertises BT.2020 primaries/matrix, which
// conflicts with the container's BT.709 declaration.
func VideoAU(index int, withColorSEI bool, annexB bool) []byte {
	isSync := index%30 == 0
	var body []byte
	nal := func(typ byte, payload []byte) {
		if annexB {
			body = append(body, 0x00, 0x00, 0x00, 0x01)
		} else {
			body = binary.BigEndian.AppendUint32(body, uint32(len(payload)+1))
		}
		body = append(body, typ)
		body = append(body, payload...)
	}
	// Access delimiter.
	nal(0x09, []byte{0xf0})
	if isSync {
		nal(0x67, []byte{0x42, 0x00, 0x1e, 0x9a, 0x66, 0x0a, 0x0f, 0xff})
		nal(0x68, []byte{0xce, 0x3c, 0x80})
	}
	if index%10 == 0 {
		nal(0x06, seiPayload(index, withColorSEI))
	}
	// Slice: a type-5 IDR on sync frames, type-1 otherwise, with a stable
	// per-frame payload so hashes are reproducible.
	sliceType := byte(0x41)
	if isSync {
		sliceType = 0x65
	}
	fill := make([]byte, 48)
	for i := range fill {
		fill[i] = byte(index*31 + i*7)
	}
	nal(sliceType, fill)
	return body
}

// seiPayload builds an SEI payload (type 5 user data unregistered) plus, when
// requested, a colour declaration in a private type 5 payload. It is escaped
// so the caller can place it directly in an Annex B stream.
func seiPayload(index int, withColor bool) []byte {
	var out []byte
	// midpoint marker every 10 frames
	msg := make([]byte, 16)
	copy(msg, []byte("mdia-timestamp!!"))
	binary.BigEndian.PutUint64(msg[8:], uint64(index))
	// The parser looks for a fixed UUID; reuse the same one the ingest side
	// defines by re-encoding it here (the value is part of the fixture
	// contract, asserted by a test).
	copy(msg[:16], seiUUIDBytes())
	body := make([]byte, 8)
	binary.BigEndian.PutUint64(body, uint64(index)*33333) // microseconds at 30 fps
	body = append(body, []byte("frame")...)
	msg = append(seiUUIDBytes(), body...)
	out = appendSEI(out, 5, msg)
	if withColor {
		// colour_type 'nclx' then primaries/transfer/matrix = BT.2020 (9,14,9)
		col := []byte{'n', 'c', 'l', 'x', 0, 9, 0, 14, 0, 9, 0x80}
		out = appendSEI(out, 5, col)
	}
	out = append(out, 0x80)
	return rbspEscape(out)
}

func appendSEI(b []byte, typ int, payload []byte) []byte {
	for typ >= 255 {
		b = append(b, 0xff)
		typ -= 255
	}
	b = append(b, byte(typ))
	n := len(payload)
	for n >= 255 {
		b = append(b, 0xff)
		n -= 255
	}
	b = append(b, byte(n))
	return append(b, payload...)
}

func rbspEscape(b []byte) []byte {
	out := make([]byte, 0, len(b)+8)
	zeros := 0
	for _, v := range b {
		if zeros >= 2 && v <= 3 {
			out = append(out, 3)
			zeros = 0
		}
		out = append(out, v)
		if v == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// seiUUIDBytes mirrors ingest's private UUID. It is duplicated deliberately:
// fixtures are an independent producer of the wire format.
func seiUUIDBytes() []byte {
	return []byte{0x6d, 0x65, 0x64, 0x69, 0x61, 0x2d, 0x74, 0x73, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01}
}

// AudioFrame builds one deterministic audio frame. PCM-style so the audio
// stage can compute frame boundaries exactly.
func AudioFrame(index int, dropIfMissing bool) []byte {
	n := 192 // 48 kHz / 250 frames per second
	out := make([]byte, n)
	seed := uint32(index)*2654435761 + 12345
	for i := 0; i < n; i++ {
		seed = seed*1664525 + 1013904223
		out[i] = byte(seed >> 24)
	}
	if dropIfMissing && index < 0 {
		return nil
	}
	return out
}

// DescribeFault renders a fault for diagnostics.
func DescribeFault(f Fault) string {
	switch f {
	case FaultWrap:
		return "32-bit RTP timestamp wraps mid-program"
	case FaultLoss:
		return "RTP packets dropped and reordered"
	case FaultColorConflict:
		return "container BT.709 vs SEI BT.2020"
	case FaultBackward:
		return "timestamp jump two seconds backwards"
	case FaultAudioGap:
		return "audio frames missing"
	case FaultAudioOverlap:
		return "audio frame duplicated/overlapping"
	case FaultAudioShort:
		return "audio track ends short of a whole frame"
	case FaultCorruptLayer:
		return "top ladder layer's fragment is corrupt"
	default:
		return fmt.Sprintf("unknown fault %q", f)
	}
}
