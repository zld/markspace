package evidence

import (
	"encoding/binary"
	"testing"
)

// findBox returns the payload range of the first box with the given type.
func findBox(raw []byte, boxType string) (start, end int, ok bool) {
	for off := 0; off+8 <= len(raw); {
		size := int(binary.BigEndian.Uint32(raw[off:]))
		typ := string(raw[off+4 : off+8])
		if size == 0 || off+size > len(raw) {
			return 0, 0, false
		}
		if typ == boxType {
			return off + 8, off + size, true
		}
		off += size
	}
	return 0, 0, false
}

func extractMDAT(t *testing.T, raw []byte) []byte {
	t.Helper()
	start, end, ok := findBox(raw, "mdat")
	if !ok {
		t.Fatal("mdat not found")
	}
	return raw[start:end]
}

func extractFramePayloads(t *testing.T, raw []byte) [][]byte {
	t.Helper()
	mdat := extractMDAT(t, raw)
	var out [][]byte
	for off := 0; off+8 <= len(mdat); {
		w := binary.BigEndian.Uint16(mdat[off+4:])
		h := binary.BigEndian.Uint16(mdat[off+6:])
		size := 8 + int(w)*int(h)*3
		if off+size > len(mdat) {
			t.Fatalf("frame %d exceeds mdat", len(out))
		}
		out = append(out, mdat[off:off+size])
		off += size
	}
	return out
}
