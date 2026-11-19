package psi

import (
	"testing"
)

// buildSection assembles a syntactically valid PSI section from tableID,
// extension, version, currentNext, section numbers, and a body whose CRC is
// computed last.
func buildSection(tableID uint8, extension uint16, version uint8, currentNext bool,
	sectionNumber, lastSectionNumber uint8, body []byte) []byte {
	sectionLength := sectionSyntaxSize + len(body) + sectionCRCSize
	b := []byte{
		tableID,
		0xb0 | byte(sectionLength>>8),
		byte(sectionLength),
		byte(extension >> 8), byte(extension),
		0xc0 | version<<1 | boolBit(currentNext),
		sectionNumber,
		lastSectionNumber,
	}
	b = append(b, body...)
	crc := CRC32(b)
	return append(b, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func boolBit(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func TestCRC32KnownVector(t *testing.T) {
	// A CRC-protected section must self-validate to zero over all bytes.
	// Using the PAT shape from ISO/IEC 13818-1 Annex B gives a stable vector.
	body := []byte{0x00, 0x01, 0xe1, 0x00}
	s := buildSection(TableIDPAT, 1, 0, true, 0, 0, body)
	if got := CRC32(s); got != 0 {
		t.Fatalf("CRC32 over complete section = %#x, want 0", got)
	}
	if got := CRC32([]byte("123456789")); got != 0x0376e6e7 {
		t.Fatalf("CRC32 check vector = %#x, want 0x0376e6e7", got)
	}
}
