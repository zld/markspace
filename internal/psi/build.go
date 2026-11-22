package psi

// Builders for well-formed PSI sections. They are used both by the fixture
// generator and by tests so that every section fed to the parsers carries a
// correct CRC and a spec-compliant common header.

// appendCRC computes the CRC-32/MPEG-2 over b and appends it big-endian.
func appendCRC(b []byte) []byte {
	crc := crc32MPEG2(b)
	return append(b, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

// sectionHeader writes the 8-byte common PSI header prefix (table id through
// last_section_number). The caller must already know section_length, which
// covers bytes from transport_stream_id onward including the CRC.
func sectionHeader(tableID uint8, id uint16, version, num, last uint8, sectionLength int) []byte {
	b := make([]byte, 8)
	b[0] = tableID
	b[1] = 0xb0 | byte((sectionLength>>8)&0x0f)
	b[2] = byte(sectionLength)
	b[3] = byte(id >> 8)
	b[4] = byte(id)
	b[5] = 0xc1 | (version&0x1f)<<1 // reserved 11, current_next_indicator = 1
	b[6] = num
	b[7] = last
	return b
}

// BuildPATSection builds one CRC-valid PAT section carrying the given
// program/PMT-PID entries. networkPID (0x1fff if unused) is emitted as the
// program_number 0 entry when below 0x1fff.
func BuildPATSection(tsid uint16, version, num, last uint8, programs []PATProgram, networkPID uint16) []byte {
	var body []byte
	if networkPID < 0x1fff {
		body = appendProgramEntry(body, 0, networkPID)
	}
	for _, p := range programs {
		body = appendProgramEntry(body, p.ProgramNumber, p.PMTPID)
	}
	sectionLength := 5 + len(body) + 4 // fixed header tail, entries, CRC
	b := sectionHeader(TableIDPAT, tsid, version, num, last, sectionLength)
	b = append(b, body...)
	return appendCRC(b)
}

func appendProgramEntry(b []byte, program, pid uint16) []byte {
	return append(b,
		byte(program>>8), byte(program),
		0xe0|byte((pid>>8)&0x1f), byte(pid))
}

// CorruptCRC flips a byte inside the CRC field so verification fails.
func CorruptCRC(section []byte) []byte {
	out := append([]byte(nil), section...)
	out[len(out)-1] ^= 0xff
	return out
}
