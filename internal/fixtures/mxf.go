package fixtures

// --- MXF KLV fixture writer (private-use subset, mirrors parse/mxf.go) ---

var mxfKeyPrefix = []byte{0x06, 0x0E, 0x2B, 0x34, 0x02, 0x05, 0x01, 0x01, 0x0D, 0x01, 0x02, 0x01, 0x7F, 0x01}

func klv(item byte, val []byte) []byte {
	key := append(append([]byte{}, mxfKeyPrefix...), item, 0x01)
	var ln []byte
	switch {
	case len(val) < 0x80:
		ln = []byte{byte(len(val))}
	case len(val) < 0x100:
		ln = []byte{0x81, byte(len(val))}
	default:
		ln = []byte{0x82, byte(len(val) >> 8), byte(len(val))}
	}
	return concat(key, ln, val)
}

func buildMXF(seed int64, tIdx int, s trackSpec) []byte {
	n := s.samples
	var sampleTbl, essence []byte
	for i := 0; i < n; i++ {
		sz := sampleSize(tIdx, i)
		sampleTbl = append(sampleTbl, concat(u32(s.delta), u32(sz), []byte{1})...)
		essence = append(essence, payload(seed, tIdx, i, int(sz))...)
	}
	makeAll := func(essenceOff uint64) []byte {
		hdr := concat(
			u32(uint32(s.trackID)), u32(s.rateNum), u32(s.rateDen),
			u64(uint64(s.mediaOffset)),
			u32(s.width), u32(s.height), u32(s.rotation), u64(essenceOff),
		)
		out := klv(0x01, hdr)
		out = append(out, klv(0x02, sampleTbl)...)
		for _, c := range s.colors {
			out = append(out, klv(0x03, concat(u16(c[0]), u16(c[1]), u16(c[2])))...)
		}
		if s.crop != nil {
			out = append(out, klv(0x04, concat(u32(s.crop[0]), u32(s.crop[1]), u32(s.crop[2]), u32(s.crop[3])))...)
		}
		out = append(out, klv(0x05, rtpPacket(s.rtpBase))...)
		return out
	}
	meta := makeAll(0)
	meta = makeAll(uint64(len(meta)))
	return append(meta, essence...)
}
