package psi

import (
	"testing"
)

// buildSection constructs a CRC-valid section for tests.
func buildSection(tableID uint8, ext uint16, version uint8, current bool, number, last uint8, body []byte) Section {
	raw := []byte{
		tableID,
		0xb0,
		0, // length, filled below
		byte(ext >> 8), byte(ext),
		byte(version<<1 | boolBit(current)),
		number,
		last,
	}
	raw = append(raw, body...)
	length := len(raw) + 4 - 3
	raw[1] = 0xb0 | byte(length>>8)
	raw[2] = byte(length)
	crc := CRC32(raw)
	raw = append(raw, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
	s, err := ParseSection(raw)
	if err != nil {
		panic(err)
	}
	return s
}

func boolBit(b bool) uint8 {
	if b {
		return 1
	}
	return 0
}

func patBody(entries ...[2]uint16) []byte {
	var b []byte
	for _, e := range entries {
		b = append(b, byte(e[0]>>8), byte(e[0]), byte(0xe0|e[1]>>8), byte(e[1]))
	}
	return b
}

func pmtBody(pcr uint16, streams ...[2]uint16) []byte {
	b := []byte{byte(0xe0 | pcr>>8), byte(pcr), 0xf0, 0x00}
	for _, s := range streams {
		b = append(b, byte(s[0]), byte(0xe0|s[1]>>8), byte(s[1]), 0xf0, 0x00)
	}
	return b
}

func TestParsePAT(t *testing.T) {
	s := buildSection(TableIDPAT, 1, 3, true, 0, 0, patBody([2]uint16{1, 0x100}, [2]uint16{2, 0x200}, [2]uint16{0, 0x10}))
	pat, err := ParsePAT(s)
	if err != nil {
		t.Fatal(err)
	}
	if pat.TransportStreamID != 1 || pat.Version != 3 || len(pat.Programs) != 2 {
		t.Fatalf("unexpected PAT: %+v", pat)
	}
	if pid, ok := pat.PMTPID(2); !ok || pid != 0x200 {
		t.Fatalf("program 2 pid = %d, %v", pid, ok)
	}
	// Program 0 (NIT) must be skipped.
	if _, ok := pat.PMTPID(0); ok {
		t.Fatal("NIT entry leaked into the program map")
	}
}

func TestParsePATRejectsNonMultipleBody(t *testing.T) {
	s := buildSection(TableIDPAT, 1, 0, true, 0, 0, []byte{0x00, 0x01, 0xe0})
	if _, err := ParsePAT(s); err == nil {
		t.Fatal("expected malformed body error")
	}
}
