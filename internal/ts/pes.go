package ts

// ExtractPTS parses a PES packet payload's optional PTS (33-bit, 90 kHz).
// It returns the raw PTS and true only when the payload is a PES packet whose
// header actually carries a PTS (or PTS+DTS) field. PSI sections and payload
// fragments without a PES start indicator yield false.
func (p *Packet) ExtractPTS() (uint64, bool) {
	if !p.PayloadUnitStart || !p.HasPayload || len(p.Payload) < 9 {
		return 0, false
	}
	// PES packet start code prefix 00 00 01, followed by stream id.
	if p.Payload[0] != 0x00 || p.Payload[1] != 0x00 || p.Payload[2] != 0x01 {
		return 0, false
	}
	streamID := p.Payload[3]
	// Padding (0xBE), program stream map/directory and reserved IDs have no PTS.
	if streamID == 0xBE || streamID == 0xBF || streamID == 0xF0 ||
		streamID == 0xF1 || streamID == 0xFF || streamID < 0xC0 {
		return 0, false
	}
	// PES header data length sits at payload[8].
	hdrLen := int(p.Payload[8])
	if hdrLen <= 0 || len(p.Payload) < 9+hdrLen {
		return 0, false
	}
	flags := p.Payload[7]
	ptsDTS := (flags >> 6) & 0x03 // 0b10 = PTS only, 0b11 = PTS+DTS
	if ptsDTS != 0x02 && ptsDTS != 0x03 {
		return 0, false
	}
	base := p.Payload[9:]
	if len(base) < 5 {
		return 0, false
	}
	// 33-bit PTS spread over the marker-laden 5-byte PES field.
	pts := uint64(base[0]&0x0e)<<29 |
		uint64(base[1])<<22 |
		uint64(base[2]&0xfe)<<14 |
		uint64(base[3])<<7 |
		uint64(base[4]>>1)
	return pts, true
}
