package wav

import (
	"encoding/binary"
	"math"
)

// decodeSamples converts interleaved little-endian sample bytes to float32.
// Integer PCM is scaled by the negative full-scale value so that -32768 maps
// to exactly -1.0, matching the BS.1770 calibration convention.
//
// The returned index is the failing sample index when err != nil.
func decodeSamples(src []byte, dst []float32, h header) (int, error) {
	switch {
	case h.Format == FormatFloat:
		for i := range dst {
			v := math.Float32frombits(binary.LittleEndian.Uint32(src[i*4:]))
			if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
				return i, ErrNonFinite
			}
			dst[i] = v
		}
	case h.Bits == 16:
		for i := range dst {
			dst[i] = float32(float64(int16(binary.LittleEndian.Uint16(src[i*2:]))) / 32768.0)
		}
	case h.Bits == 24:
		for i := range dst {
			b := src[i*3:]
			v := int32(b[0]) | int32(b[1])<<8 | int32(b[2])<<16
			if v&0x800000 != 0 {
				v |= ^int32(0xFFFFFF)
			}
			dst[i] = float32(float64(v) / 8388608.0)
		}
	case h.Bits == 32:
		for i := range dst {
			v := int32(binary.LittleEndian.Uint32(src[i*4:]))
			dst[i] = float32(float64(v) / 2147483648.0)
		}
	}
	return 0, nil
}
