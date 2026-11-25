package mp4

import "encoding/binary"

// BuildTestFragment is a test-only fixture constructor (exported for tests in
// other packages). It builds one moof+mdat pair carrying a single track with
// one sample of the given size, the given mfhd sequence number and tfdt base.
func BuildTestFragment(seq, baseT uint32, mdatPayload int) []byte {
	fb := func(ver uint8, flags uint32) []byte {
		return []byte{ver, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	}
	putU32 := func(v uint32) []byte {
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, v)
		return b
	}
	mkBox := func(typ string, payload []byte) []byte {
		buf := make([]byte, 8+len(payload))
		binary.BigEndian.PutUint32(buf, uint32(8+len(payload)))
		copy(buf[4:8], typ)
		copy(buf[8:], payload)
		return buf
	}

	mfhd := mkBox("mfhd", append(fb(0, 0), putU32(seq)...))
	tfhd := mkBox("tfhd", append(fb(0, 0), putU32(1)...))
	tfdt := mkBox("tfdt", append(fb(0, 0), putU32(baseT)...))
	// flags: data-offset(0x1) | sample-duration(0x100) | sample-size(0x200)
	trunPay := append(fb(0, 0x301), putU32(1)...)
	trunPay = append(trunPay, make([]byte, 4)...) // data-offset placeholder
	trunPay = append(trunPay, putU32(100)...)     // duration
	trunPay = append(trunPay, putU32(uint32(mdatPayload))...)
	trun := mkBox("trun", trunPay)
	traf := mkBox("traf", append(append(tfhd, tfdt...), trun...))
	moof := mkBox("moof", append(mfhd, traf...))
	mdat := mkBox("mdat", make([]byte, mdatPayload))
	data := append(moof, mdat...)

	// Patch trun data offset = mdat payload offset relative to moof start (0).
	trunOff := len(moof) - len(trun)
	offPos := trunOff + 8 + 4 + 4
	binary.BigEndian.PutUint32(data[offPos:offPos+4], uint32(len(moof)+8))
	return data
}
