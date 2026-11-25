package scan

import (
	"encoding/binary"
)

// brandForTest builds a bare styp box for malformed-input tests.
func brandForTest() []byte {
	payload := make([]byte, 8)
	copy(payload[0:4], "msdh")
	return wrapBox("styp", payload)
}

// moofOnlyForTest builds a moof containing mfhd(1) and an empty traf, with no
// mdat sibling, to exercise the required-box check.
func moofOnlyForTest() []byte {
	mfhd := fullBox("mfhd", 0, 0, u32Bytes(1))
	traf := wrapBox("traf", nil)
	return wrapBox("moof", append(mfhd, traf...))
}

func wrapBox(name string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
	copy(out[4:8], name)
	copy(out[8:], payload)
	return out
}

func fullBox(name string, version uint8, flags uint32, body []byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return wrapBox(name, append(head, body...))
}

func u32Bytes(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}
