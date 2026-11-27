package fixture

import (
	"gamepack/internal/stream"
)

// genUnit is a located coding unit while the fixture is being assembled.
type genUnit struct {
	ordinal int
	start   int64 // first payload byte after the start-code prefix
	end     int64 // scanner-visible end: start of the next prefix (or EOF)
	length  int64
	key     bool
	data    []byte
}

// buildBitstream emits codec-correct synthetic units and returns the raw
// bytes, the located units and the keyframe ordinals.
func buildBitstream(opt Options) ([]byte, []genUnit, []int) {
	switch opt.Codec {
	case stream.CodecAV1:
		return buildOBU(opt)
	default:
		return buildAnnexB(opt)
	}
}

func buildAnnexB(opt Options) ([]byte, []genUnit, []int) {
	var raw []byte
	units := make([]genUnit, 0, opt.Units)
	var keys []int
	for i := 0; i < opt.Units; i++ {
		// Unit 0 carries parameter/sequence metadata; actual keyframe samples
		// begin at the first cut after it and repeat every KeyEvery units.
		key := i > 0 && i%opt.KeyEvery == 0
		var nalType byte
		if opt.Codec == stream.CodecH265 {
			nalType = hevcNalType(key, i)
		} else {
			nalType = avcNalType(key, i)
		}
		// 3-byte start code plus a small deterministic NAL payload. The final
		// byte is never 0x00 so Annex B scanning cannot mistake it for a
		// start-code prefix and trim the unit.
		payload := []byte{nalType, byte(i + 1), byte(1 + (i*7)&0xfe)}
		raw = append(raw, 0, 0, 1)
		dataStart := int64(len(raw))
		raw = append(raw, payload...)
		dataEnd := int64(len(raw))
		units = append(units, genUnit{
			ordinal: i, start: dataStart, end: dataEnd,
			length: int64(len(payload)), key: key,
			data: append([]byte(nil), payload...),
		})
		if key {
			keys = append(keys, i)
		}
	}
	return raw, units, keys
}

// avcNalType returns IDR (5) for keyframes and a non-IDR slice (1) otherwise;
// the first unit is an SPS (7) so the stream also carries parameter metadata.
func avcNalType(key bool, ordinal int) byte {
	if ordinal == 0 {
		return 0x67 // SPS
	}
	if key {
		return 0x65 // IDR slice, nal_unit_type 5
	}
	return 0x41 // non-IDR slice, nal_unit_type 1
}

// hevcNalType uses IDR_W_RADL (19) for keyframes, TRAIL_R (1) otherwise; the
// first unit is VPS/SPS-ish type 33 (SPS_NUT).
func hevcNalType(key bool, ordinal int) byte {
	if ordinal == 0 {
		return 33 << 1 // SPS_NUT encoded in the high 6 bits
	}
	if key {
		return 19 << 1
	}
	return 1 << 1
}

func buildOBU(opt Options) ([]byte, []genUnit, []int) {
	var raw []byte
	units := make([]genUnit, 0, opt.Units)
	var keys []int
	for i := 0; i < opt.Units; i++ {
		// Unit 0 is a temporal delimiter; sequence-header keyframe OBUs start
		// at the first cut and repeat every KeyEvery units.
		key := i > 0 && i%opt.KeyEvery == 0
		obuType := uint8(6)
		if i == 0 {
			obuType = 2
		} else if key {
			obuType = 1
		}
		payload := []byte{byte(i + 1), byte((i * 5) & 0xff), byte((i * 3) & 0xff)}
		header := byte(0x80) | (obuType << 3) | 0x02
		frame := []byte{header}
		frame = appendLeb(frame, uint64(len(payload)))
		frame = append(frame, payload...)

		start := int64(len(raw))
		raw = append(raw, frame...)
		end := int64(len(raw))
		units = append(units, genUnit{
			ordinal: i, start: start, end: end, length: end - start, key: key,
			data: append([]byte(nil), frame...),
		})
		if key {
			keys = append(keys, i)
		}
	}
	return raw, units, keys
}

func appendLeb(b []byte, v uint64) []byte {
	for {
		c := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			b = append(b, c|0x80)
		} else {
			return append(b, c)
		}
	}
}
