package probe

import (
	"encoding/binary"

	"archive/internal/clock"
)

// parseLocalSetItems decodes tag/length/value items until a 0x0000 terminator
// or the end of the buffer. It returns the bytes consumed.
func parseLocalSetItems(b []byte, depth int) (map[uint16][]byte, int, *clock.Error) {
	if depth > 2 {
		return nil, 0, clock.New(clock.KindOutOfRange, "local set nesting too deep")
	}
	items := make(map[uint16][]byte)
	i := 0
	for i+2 <= len(b) {
		tag := binary.BigEndian.Uint16(b[i : i+2])
		if tag == 0x0000 {
			return items, i + 2, nil
		}
		if i+2 >= len(b) {
			break
		}
		lb := b[i+2]
		var n, consumed int
		if lb&0x80 != 0 {
			k := int(lb & 0x7f)
			if k == 0 || k > 8 || i+3+k > len(b) {
				return nil, 0, clock.New(clock.KindTruncated, "malformed local set length")
			}
			n = 0
			for j := 0; j < k; j++ {
				n = n<<8 | int(b[i+3+j])
			}
			consumed = 3 + k
		} else {
			n = int(lb)
			consumed = 3
		}
		if n < 0 || n > maxLocalSet || i+consumed+n > len(b) {
			return nil, 0, clock.Newf(clock.KindTruncated, "local set item 0x%04x overruns buffer", tag)
		}
		items[tag] = b[i+consumed : i+consumed+n]
		i += consumed + n
	}
	return items, i, nil
}

func readUint(b []byte) (int64, *clock.Error) {
	if len(b) == 0 || len(b) > 8 {
		return 0, clock.Newf(clock.KindInvalidMetadata, "integer field has length %d", len(b))
	}
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	if v > uint64(1)<<62 {
		return 0, clock.New(clock.KindOverflow, "integer field exceeds 62 bits")
	}
	return int64(v), nil
}

func readInt(b []byte) (int, *clock.Error) {
	v, err := readUint(b)
	if err != nil {
		return 0, err
	}
	if v > int64(^uint(0)>>1) {
		return 0, clock.New(clock.KindOverflow, "integer field exceeds platform int")
	}
	return int(v), nil
}

func readRate(b []byte) (int32, int32, *clock.Error) {
	if len(b) != 8 {
		return 0, 0, clock.Newf(clock.KindInvalidMetadata, "edit rate must be 8 bytes, got %d", len(b))
	}
	num := int32(binary.BigEndian.Uint32(b[0:4]))
	den := int32(binary.BigEndian.Uint32(b[4:8]))
	if num <= 0 || den <= 0 {
		return 0, 0, clock.Newf(clock.KindInvalidMetadata, "non-positive edit rate %d/%d", num, den)
	}
	return num, den, nil
}

func frameNS(n int64, num, den int32) int64 {
	if num <= 0 || den <= 0 {
		return 0
	}
	tb, err := clock.NewTimebase(uint32(num))
	if err != nil {
		return 0
	}
	ns, err := tb.ToNanos(n * int64(den))
	if err != nil {
		return 0
	}
	return ns
}
