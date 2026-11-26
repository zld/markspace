package ingest

// OpusTestPayload builds an Opus packet with the given TOC config/code and
// enough trailing bytes to satisfy multi-frame code-3 parsing.
func OpusTestPayload(config int, code byte) []byte {
	toc := byte(config<<3) | (code & 0x03)
	switch code {
	case 3:
		return []byte{toc, 2, 0xaa, 0xbb, 0xcc}
	default:
		return []byte{toc, 0xaa, 0xbb, 0xcc, 0xdd}
	}
}

// AACTestPayload wraps AAC access units of the given sizes using the same
// RFC 3640 framing as the production parser.
func AACTestPayload(auSizes ...int) []byte {
	var out []byte
	headerBits := uint16(len(auSizes) * 16)
	out = append(out, byte(headerBits>>8), byte(headerBits))
	for _, sz := range auSizes {
		h := uint16(sz << 3)
		out = append(out, byte(h>>8), byte(h))
	}
	for i, sz := range auSizes {
		for b := 0; b < sz; b++ {
			out = append(out, byte(i+b))
		}
	}
	return out
}
