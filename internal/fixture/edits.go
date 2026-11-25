package fixture

// EditSpec is the fixture-builder view of an edit-list entry.
type EditSpec struct {
	Duration  uint32
	MediaTime int32
}

func elstV0Box(entries []EditSpec) []byte {
	b := builder{}
	b.u32(uint32(len(entries)))
	for _, e := range entries {
		b.u32(e.Duration)
		b.u32(uint32(e.MediaTime))
		b.u16(1) // media rate integer
		b.u16(0) // fraction
	}
	return box("elst", fullBox(0, 0, b.buf))
}

// rawElstV1NegativeDuration builds a version-1 elst whose duration has the
// high bit set, i.e. its signed int64 interpretation is negative.
func rawElstV1NegativeDuration() []byte {
	b := builder{}
	b.u32(1) // entry count
	b.u64(1 << 63)
	b.u64(250)
	b.u16(1)
	b.u16(0)
	return box("elst", fullBox(1, 0, b.buf))
}

func edtsBox(children ...[]byte) []byte {
	var payload []byte
	for _, c := range children {
		payload = append(payload, c...)
	}
	return box("edts", payload)
}
