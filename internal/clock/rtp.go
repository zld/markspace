package clock

// ExtendRTPTimestamp resolves a 32-bit RTP timestamp against the previously
// extended timestamp. It follows the usual signed 32-bit delta rule and
// therefore handles wrap-around and modest reordering.
func ExtendRTPTimestamp(raw uint32, previousExtended uint64) uint64 {
	previousRaw := uint32(previousExtended)
	delta := int32(raw - previousRaw)
	next := int64(previousExtended) + int64(delta)
	if next < 0 {
		return 0
	}
	return uint64(next)
}

// ExtendSequenceNumber resolves a 16-bit RTP sequence number against the
// previously extended sequence number.
func ExtendSequenceNumber(raw uint16, previousExtended uint64) uint64 {
	previousRaw := uint16(previousExtended)
	delta := int16(raw - previousRaw)
	next := int64(previousExtended) + int64(delta)
	if next < 0 {
		return 0
	}
	return uint64(next)
}
