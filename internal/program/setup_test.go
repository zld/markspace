package program

import (
	"testing"

	"tsinspect/internal/psi"
)

// section builds a CRC-valid PAT or PMT section for state tests.
func section(tableID uint8, extension uint16, version uint8, currentNext bool,
	sectionNumber, lastSectionNumber uint8, body []byte) *psi.Section {
	cn := byte(0)
	if currentNext {
		cn = 1
	}
	sectionLength := psi.SyntaxSize + len(body) + psi.CRCSize
	hdr := []byte{
		tableID,
		0xb0 | byte(sectionLength>>8), byte(sectionLength),
		byte(extension >> 8), byte(extension),
		0xc0 | version<<1 | cn, sectionNumber, lastSectionNumber,
	}
	full := append(hdr, body...)
	crc := psi.CRC32(full)
	full = append(full, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	parsed, err := psi.ParseSection(full)
	if err != nil {
		panic(err)
	}
	return parsed
}

func patBody(programs ...uint16) []byte { // pairs: program, pmt pid
	var b []byte
	for i := 0; i < len(programs); i += 2 {
		p, pid := programs[i], programs[i+1]
		b = append(b, byte(p>>8), byte(p), 0xe0|byte(pid>>8), byte(pid))
	}
	return b
}

func pmtBody(pcr uint16, streams ...uint8) []byte { // triples: stream_type, pid hi, pid lo
	b := []byte{0xe0 | byte(pcr>>8), byte(pcr), 0xf0, 0x00}
	for i := 0; i < len(streams); i += 3 {
		b = append(b, streams[i], 0xe0|byte(streams[i+1]>>0)&0x00|byte(streams[i+1]&0x1f), streams[i+2], 0xf0, 0x00)
	}
	return b
}

func applyPAT(t *testing.T, m *Manager, version uint8, programs ...uint16) {
	t.Helper()
	if err := m.ApplyPAT(section(psi.TableIDPAT, 1, version, true, 0, 0, patBody(programs...))); err != nil {
		t.Fatalf("ApplyPAT v%d: %v", version, err)
	}
}
