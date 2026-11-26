package ts

import (
	"rtpregress/internal/clock"
)

// pts33Mod is the 33-bit MPEG PTS/DTS period in 90 kHz ticks.
const pts33Mod int64 = 1 << 33

// pts33Unwrapper expands 33-bit PTS values onto a monotonic 90 kHz line.
type pts33Unwrapper struct {
	last int64
	raw  int64
	has  bool
}

// Unwrap returns the monotonic tick count for a raw 33-bit PTS.
func (u *pts33Unwrapper) Unwrap(raw int64) int64 {
	if !u.has {
		u.has, u.raw, u.last = true, raw, raw
		return raw
	}
	if raw < u.raw && u.raw > pts33Mod/2 && raw < pts33Mod/4 {
		u.last += pts33Mod + (raw - u.raw) // wrapped near the period boundary
	} else {
		u.last += raw - u.raw
	}
	u.raw = raw
	return u.last
}

// parsePESHeader extracts raw (still wrapped) PTS/DTS ticks from a PES packet.
// Layout: 00 00 01 stream_id | len(2) | flags1 | flags2(PTS_DTS) | hdr_len
// Returns hasDTS=false when only a PTS is present.
func parsePESHeader(buf []byte) (pts, dts int64, hasDTS bool, err error) {
	if len(buf) < 9 || buf[0] != 0x00 || buf[1] != 0x00 || buf[2] != 0x01 {
		return 0, 0, false, clock.New(clock.CodeTSPES, "ts:pes", -1, 0, 0,
			"bad PES start code")
	}
	// buf[6] flags1: 10xxxxxx means an optional PES header follows
	if buf[6]&0xC0 != 0x80 {
		return 0, 0, false, clock.New(clock.CodeTSPES, "ts:pes", -1, 0, 0,
			"PES optional header absent")
	}
	// buf[7] flags2 carries the 2-bit PTS_DTS_flags in its top nibble
	ptsDTSFlags := buf[7] >> 6 & 0x03
	hdrLen := int(buf[8])
	if 9+hdrLen > len(buf) {
		return 0, 0, false, clock.New(clock.CodeTSTruncated, "ts:pes", -1, 0, 0,
			"PES header length %d overruns buffer", hdrLen)
	}
	pts, err = readTS(buf[9:])
	if err != nil {
		return 0, 0, false, err
	}
	switch ptsDTSFlags {
	case 0x02: // PTS only ('0010')
		return pts, 0, false, nil
	case 0x03: // PTS + DTS ('0011')
		if 14 > 9+hdrLen {
			return 0, 0, false, clock.New(clock.CodeTSPES, "ts:pes", -1, 0, 0,
				"DTS marker but no room for DTS")
		}
		dts, err = readTS(buf[14:])
		if err != nil {
			return 0, 0, false, err
		}
		return pts, dts, true, nil
	default:
		return 0, 0, false, clock.New(clock.CodeTSPES, "ts:pes", -1, 0, 0,
			"forbidden PTS/DTS flags 0x%x", ptsDTSFlags)
	}
}

// readTS decodes one 5-byte 33-bit PTS/DTS field.
func readTS(b []byte) (int64, error) {
	if len(b) < 5 {
		return 0, clock.New(clock.CodeTSTruncated, "ts:pes", -1, 0, 0,
			"timestamp field truncated")
	}
	ts := int64(b[0]&0x0E)<<29 | int64(b[1])<<22 | int64(b[2]&0xFE)<<14 |
		int64(b[3])<<7 | int64(b[4])>>1
	return ts, nil
}

// trimPESPayload clips a PUSI TS packet payload to the declared PES length so
// trailing TS packet stuffing is not buffered as elementary stream bytes.
// A zero/unbounded length (video) is returned unchanged.
func trimPESPayload(payload []byte) []byte {
	if len(payload) < 6 || payload[0] != 0x00 || payload[1] != 0x00 || payload[2] != 0x01 {
		return payload
	}
	declared := int(uint16(payload[4])<<8 | uint16(payload[5]))
	if declared == 0 {
		return payload
	}
	// the length field covers everything after its own 2 bytes
	total := 6 + declared
	if total >= len(payload) {
		return payload // PES fills or exceeds the packet: no stuffing to trim
	}
	out := make([]byte, total)
	copy(out, payload[:total])
	return out
}

// extractES returns elementary stream bytes after the full PES header.
// For a bounded PES (audio always carries a length), the result is clipped to
// the declared PES_packet_length so TS packet stuffing is never treated as ES.
func extractES(buf []byte) []byte {
	if len(buf) < 9 || buf[0] != 0x00 || buf[1] != 0x00 || buf[2] != 0x01 {
		return nil
	}
	hdrLen := int(buf[8])
	start := 9 + hdrLen
	if start >= len(buf) {
		return nil
	}
	declared := int(uint16(buf[4])<<8 | uint16(buf[5]))
	if declared > 0 {
		// declared counts bytes after its own 2-byte field: flags1,flags2,
		// hdrLen, optional header and ES.
		esLen := declared - 3 - hdrLen
		if esLen <= 0 {
			return nil
		}
		end := start + esLen
		if end > len(buf) {
			end = len(buf) // truncated PES: take what survived
		}
		out := make([]byte, end-start)
		copy(out, buf[start:end])
		return out
	}
	out := make([]byte, len(buf)-start)
	copy(out, buf[start:])
	return out
}
