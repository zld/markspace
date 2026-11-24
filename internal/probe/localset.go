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
