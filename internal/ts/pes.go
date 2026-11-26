package ts

// parsePES decodes the small subset of PES headers carrying PTS/DTS that the
// kernel needs for boundary cross-checks. It never fabricates timestamps: if
// the header is malformed it returns nil.
func parsePES(p []byte) *PESHeader {
	if len(p) < 9 || p[0] != 0x00 || p[1] != 0x00 || p[2] != 0x01 {
		return nil
	}
	h := &PESHeader{StreamID: p[3]}
	pesHeaderLen := int(p[8])
	h.HeaderLen = 9 + pesHeaderLen
	if len(p) < 9+pesHeaderLen {
		return h
	}
	ptsDTSFlags := p[7] & 0xC0
	pos := 9
	switch ptsDTSFlags {
	case 0x80: // PTS only
		if len(p) < pos+5 {
			return h
		}
		h.PTS = decodeTS(p[pos : pos+5])
		h.HasPTS = true
	case 0xC0: // PTS + DTS
		if len(p) < pos+10 {
			return h
		}
		h.PTS = decodeTS(p[pos : pos+5])
		h.DTS = decodeTS(p[pos+5 : pos+10])
		h.HasPTS, h.HasDTS = true, true
	}
	return h
}

// decodeTS decodes the standard 5-byte PTS/DTS field into a 33-bit value.
func decodeTS(b []byte) uint64 {
	_ = b[4] // bounds hint
	return (uint64(b[0]&0x0E) << 29) |
		(uint64(b[1]) << 22) |
		(uint64(b[2]&0xFE) << 14) |
		(uint64(b[3]) << 7) |
		(uint64(b[4]) >> 1)
}

// EncodeTS encodes a 33-bit timestamp using the given 4-bit marker nibble
// (0x2 for PTS-only, 0x3 for PTS of PTS+DTS, 0x1 for DTS).
func EncodeTS(marker byte, ts uint64) [5]byte {
	return [5]byte{
		marker<<4 | byte((ts>>30)&0x07)<<1 | 0x01,
		byte((ts >> 22) & 0xFF),
		byte((ts>>14)&0xFE) | 0x01, // PTS[21..15] shifted into bits [7:1]
		byte((ts >> 7) & 0xFF),
		byte(ts<<1)&0xFE | 0x01,
	}
}
