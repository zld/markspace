package ebml

import "math"

// float64BE encodes f as an 8-byte big endian IEEE-754 double (EBML float).
func float64BE(f float64) [8]byte {
	bits := math.Float64bits(f)
	var out [8]byte
	for i := 7; i >= 0; i-- {
		out[i] = byte(bits & 0xFF)
		bits >>= 8
	}
	return out
}

// readFloat64BE decodes an EBML float payload of width 0, 4 or 8 bytes.
func readFloat64BE(p []byte) (float64, bool) {
	switch len(p) {
	case 0:
		return 0, true
	case 4:
		var bits uint32
		for _, b := range p {
			bits = bits<<8 | uint32(b)
		}
		return float64(math.Float32frombits(bits)), true
	case 8:
		var bits uint64
		for _, b := range p {
			bits = bits<<8 | uint64(b)
		}
		return math.Float64frombits(bits), true
	default:
		return 0, false
	}
}

// readUint decodes an EBML unsigned integer payload (big endian, 1..8 bytes).
func readUint(p []byte) (uint64, bool) {
	if len(p) == 0 || len(p) > 8 {
		return 0, false
	}
	var v uint64
	for _, b := range p {
		v = v<<8 | uint64(b)
	}
	return v, true
}
