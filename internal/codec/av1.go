package codec

import "mediacheck/internal/fault"

// parseAV1 parses low-overhead OBUs: 1-byte header, optional temporal
// id / spatial id byte, leb128 payload size, payload. Keyframe
// detection uses the first frame-OBU payload bits (frame_type == 0),
// which is the convention our own sample generator follows.
func parseAV1(data []byte) ([]Unit, error) {
	var units []Unit
	i := 0
	for i < len(data) {
		hdr := data[i]
		o := int64(i)
		if hdr&0x80 != 0 {
			return nil, fault.New(fault.ErrBadHeader, o, "obu_forbidden_bit set")
		}
		if hdr&0x01 != 0 {
			return nil, fault.New(fault.ErrBadHeader, o, "obu_reserved_1bit set")
		}
		typ := (hdr >> 3) & 0x0F
		hasExt := hdr&0x04 != 0
		hasSize := hdr&0x02 != 0
		i++
		if hasExt {
			if i >= len(data) {
				return nil, fault.New(fault.ErrTruncatedUnit, int64(len(data)), "obu_extension truncated")
			}
			i++
		}
		if !hasSize {
			return nil, fault.New(fault.ErrBadHeader, o, "obu_has_size_field must be 1")
		}
		size, n, ok := leb128(data[i:])
		if !ok {
			return nil, fault.New(fault.ErrTruncatedUnit, int64(len(data)), "leb128 size truncated")
		}
		i += n
		if i+size > len(data) {
			return nil, fault.New(fault.ErrTruncatedUnit, int64(len(data)),
				"obu at %d declares %d payload bytes, only %d remain", o, size, len(data)-i)
		}
		u := Unit{Offset: o, StartCode: o, Size: 1 + n + size, Type: typ, Name: av1Name(typ)}
		if typ == 6 && size > 0 && data[i]>>6 == 0 { // frame OBU, frame_type KEY
			u.Keyframe = true
		}
		units = append(units, u)
		i += size
	}
	return units, nil
}

// leb128 decodes an unsigned leb128 value; ok=false on truncation.
func leb128(b []byte) (val, n int, ok bool) {
	for n = 0; n < 8 && n < len(b); n++ {
		val |= int(b[n]&0x7F) << (7 * n)
		if b[n]&0x80 == 0 {
			return val, n + 1, true
		}
	}
	return 0, 0, false
}

func av1Name(t uint8) string {
	switch t {
	case 1:
		return "seq_header"
	case 2:
		return "temporal_delimiter"
	case 3:
		return "frame_header"
	case 6:
		return "frame"
	case 15:
		return "padding"
	}
	return "obu"
}
