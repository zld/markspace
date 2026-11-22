package psi

// MaxSectionLength bounds section_length for private sections (PAT/PMT
// use at most 1021/4093; 4093 covers both).
const MaxSectionLength = 4093

// Section is one complete, CRC-valid PSI section.
type Section struct {
	TableID           uint8
	TableIDExtension  uint16
	Version           uint8
	CurrentNext       bool
	SectionNumber     uint8
	LastSectionNumber uint8
	Body              []byte // after the 8-byte header, before the CRC
	Raw               []byte // full section including CRC
}

// Assembler reassembles PSI sections from the payload of one PID,
// handling pointer fields and sections spanning multiple packets.
type Assembler struct {
	buf []byte
}

// Feed consumes one TS payload. pusi is the payload_unit_start_indicator
// of the packet. It returns the sections completed by this payload and
// the number of sections dropped because their CRC did not validate.
func (a *Assembler) Feed(pusi bool, payload []byte) (out []Section, crcErrors int) {
	if len(payload) == 0 {
		return nil, 0
	}
	if pusi {
		pointer := int(payload[0])
		payload = payload[1:]
		if pointer > len(payload) {
			a.buf = nil // corrupt pointer field
			return nil, 0
		}
		if pointer > 0 && len(a.buf) > 0 {
			// Pointer bytes complete the previous section.
			a.buf = append(a.buf, payload[:pointer]...)
			out, crcErrors = a.drain()
		}
		// A unit start abandons any unfinished previous section.
		a.buf = nil
		payload = payload[pointer:]
	}
	if len(payload) > 0 {
		a.buf = append(a.buf, payload...)
	}
	secs, errs := a.drain()
	return append(out, secs...), crcErrors + errs
}

// drain extracts every complete section currently buffered.
func (a *Assembler) drain() (out []Section, crcErrors int) {
	for {
		if len(a.buf) == 0 {
			return out, crcErrors
		}
		if a.buf[0] == 0xFF { // stuffing
			a.buf = nil
			return out, crcErrors
		}
		if len(a.buf) < 3 {
			return out, crcErrors
		}
		secLen := int(a.buf[1]&0x0F)<<8 | int(a.buf[2])
		if secLen > MaxSectionLength {
			a.buf = nil // implausible length: drop the buffer
			return out, crcErrors
		}
		total := 3 + secLen
		if len(a.buf) < total {
			return out, crcErrors // wait for more packets
		}
		raw := append([]byte(nil), a.buf[:total]...)
		a.buf = a.buf[total:]
		if s, ok := parseSection(raw); ok {
			out = append(out, s)
		} else {
			crcErrors++
		}
	}
}

// parseSection validates the CRC and decodes the generic header of a
// long-form (section_syntax_indicator set) section.
func parseSection(raw []byte) (Section, bool) {
	var s Section
	if len(raw) < 12 || raw[1]&0x80 == 0 {
		return s, false
	}
	if Checksum(raw) != 0 {
		return s, false
	}
	s = Section{
		TableID:           raw[0],
		TableIDExtension:  uint16(raw[3])<<8 | uint16(raw[4]),
		Version:           (raw[5] >> 1) & 0x1F,
		CurrentNext:       raw[5]&0x01 != 0,
		SectionNumber:     raw[6],
		LastSectionNumber: raw[7],
		Body:              raw[8 : len(raw)-4],
		Raw:               raw,
	}
	return s, true
}
