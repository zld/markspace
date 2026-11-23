package fixture

import "encoding/binary"

// buildTruncatedFragment emits a minimal fMP4 moof/traf/tfhd structure whose
// declared tfhd size extends beyond the bytes actually present. The MP4
// adapter must reject this as an unrecoverable truncated fragment.
func buildTruncatedFragment() []byte {
	fullBox := func(version byte, flags uint32) []byte {
		return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	}
	box := func(typ string, payload []byte) []byte {
		b := make([]byte, 8+len(payload))
		binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
		copy(b[4:8], typ)
		copy(b[8:], payload)
		return b
	}
	tfhdPayload := append(fullBox(0, 0), 0, 0, 0, 1) // track id 1
	tfhd := box("tfhd", tfhdPayload)
	// Declare a traf that claims more space than the fragment actually carries.
	traf := box("traf", tfhd)
	moof := box("moof", traf)
	// Cut 6 bytes off so the last declared box overruns EOF.
	return moof[:len(moof)-6]
}
