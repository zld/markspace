package ebml

// EncodeVINT encodes value as an EBML variable-length integer using the
// narrowest legal width. The all-ones reserved marker is avoided by widening
// when the value equals the width's reserved value.
func EncodeVINT(value uint64) []byte {
	for width := 1; width <= 8; width++ {
		bits := 7*width - 1
		if bits >= 64 {
			bits = 63
		}
		maxVal := uint64(1)<<bits - 1
		if value <= maxVal {
			out := make([]byte, width)
			v := value
			for i := width - 1; i >= 0; i-- {
				out[i] = byte(v & 0xFF)
				v >>= 8
			}
			out[0] |= 0x80 >> (width - 1)
			// Reserved all-ones marker: widen instead.
			if isReservedMarker(out) {
				continue
			}
			return out
		}
	}
	return nil
}

// UnknownVINT returns the unknown-length size VINT of the given width
// (1..8), e.g. width 1 -> 0xFF, width 2 -> 0x7F 0xFF, width 3 -> 0x3FFFFFF.
func UnknownVINT(width int) []byte {
	out := make([]byte, width)
	for i := range out {
		out[i] = 0xFF
	}
	if width > 1 {
		// Keep only the single high marker bit in the leading byte; for
		// width 1 the marker bit and all-ones data coincide as 0xFF.
		out[0] = 0x80 >> uint(width-1)
	}
	return out
}

func isReservedMarker(b []byte) bool {
	for _, x := range b {
		if x != 0xFF {
			return false
		}
	}
	return true
}

// EncodeID encodes an EBML element ID. Unlike a numeric VINT, an EBML ID is
// a fixed-width VINT whose length-marker bit is part of the identifier. The
// canonical Matroska IDs already carry their marker bit, so width is derived
// from the value's significant bytes rather than re-derived by masking.
func EncodeID(id uint32) []byte {
	switch {
	case id > 0x00FFFFFF:
		return []byte{byte(id >> 24), byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0x0000FFFF:
		return []byte{byte(id >> 16), byte(id >> 8), byte(id)}
	case id > 0x000000FF:
		return []byte{byte(id >> 8), byte(id)}
	default:
		return []byte{byte(id)}
	}
}

// EncodeElement serializes one element with a known payload length.
func EncodeElement(id uint32, payload []byte) []byte {
	out := append(EncodeID(id), EncodeVINT(uint64(len(payload)))...)
	return append(out, payload...)
}

// EncodeUnknownElement serializes an element whose size VINT carries the
// unknown-length marker; payload runs to the enclosing boundary.
func EncodeUnknownElement(id uint32, width int, payload []byte) []byte {
	out := append(EncodeID(id), UnknownVINT(width)...)
	return append(out, payload...)
}
