package analyze

import (
	"bytes"
	"testing"

	"github.com/example/tsaudit/internal/psi"
)

// packet builders mirror the on-disk TS layout used by the fixture generator.
func hdr(pid uint16, pusi bool, cc uint8) [4]byte {
	b1 := byte(pid>>8) & 0x1f
	if pusi {
		b1 |= 0x40
	}
	return [4]byte{0x47, b1, byte(pid), 0x10 | (cc & 0x0f)}
}

func psiPacket(pid uint16, cc uint8, section []byte) [188]byte {
	var p [188]byte
	h := hdr(pid, true, cc)
	copy(p[0:4], h[:])
	p[4] = 0x00 // pointer
	copy(p[5:], section)
	for i := 5 + len(section); i < 188; i++ {
		p[i] = 0xff
	}
	return p
}

func esPacket(pid uint16, cc uint8) [188]byte {
	var p [188]byte
	h := hdr(pid, false, cc)
	copy(p[0:4], h[:])
	return p
}

func finishSection(body []byte) []byte {
	s := append([]byte{}, body...)
	s = append(s, 0, 0, 0, 0)
	length := len(s) - 3
	s[1] = (s[1] & 0xf0) | byte(length>>8)
	s[2] = byte(length)
	crc := psi.CRC32MPEG2(s[:len(s)-4])
	s[len(s)-4] = byte(crc >> 24)
	s[len(s)-3] = byte(crc >> 16)
	s[len(s)-2] = byte(crc >> 8)
	s[len(s)-1] = byte(crc)
	return s
}

func patSection(version uint8) []byte {
	body := []byte{0x00, 0xf0, 0x00, 0x00, 0x01, 0xc1 | version<<1, 0x00, 0x00,
		0x00, 0x01, 0xf0, 0x00} // program 1 -> PMT PID 4096
	return finishSection(body)
}

func pmtSection(version uint8, videoPID uint16) []byte {
	body := []byte{
		0x02, 0xf0, 0x00, 0x00, 0x01, 0xc1 | version<<1, 0x00, 0x00,
		0xe1, 0x00, // PCR PID 256
		0xf0, 0x00, // program info length 0
		0x1b, byte(0xe0 | videoPID>>8), byte(videoPID), 0xf0, 0x00,
		0x0f, 0xe1, 0x01, 0xf0, 0x00, // audio PID 257
	}
	return finishSection(body)
}

func TestEndToEndRemap(t *testing.T) {
	var buf bytes.Buffer
	pat := patSection(0)
	pmt0 := pmtSection(0, 256)
	pmt1 := pmtSection(1, 258)

	write := func(p [188]byte) { buf.Write(p[:]) }
	write(psiPacket(0, 0, pat))
	write(psiPacket(4096, 0, pmt0))
	for i := 0; i < 12; i++ {
		write(esPacket(256, uint8(i)))
	}
	for i := 0; i < 20; i++ {
		write(esPacket(257, uint8(i)))
	}
	write(psiPacket(4096, 1, pmt1))
	for i := 0; i < 8; i++ {
		write(esPacket(258, uint8(i)))
	}

	sum, err := New("mem", &buf).Run(false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.ExitCode != ExitOK {
		t.Fatalf("exit=%d want 0, errors=%v", sum.ExitCode, sum.StructuralErrors)
	}
	if !sum.HasCompletePAT || sum.PATVersionChanges != 0 || sum.PMTVersionChanges != 1 {
		t.Fatalf("version accounting wrong: pat=%d pmt=%d completePAT=%v",
			sum.PATVersionChanges, sum.PMTVersionChanges, sum.HasCompletePAT)
	}
	counts := map[uint16]int64{}
	for _, c := range sum.PIDCounters {
		counts[c.PID] = c.TotalPackets
	}
	if counts[256] != 12 || counts[258] != 8 || counts[257] != 20 {
		t.Fatalf("PID counts wrong: %+v", counts)
	}
	// Four intervals: 256 v0, 257 v0, 257 v1 (remap boundary), 258 v1.
	if len(sum.Mappings) != 4 {
		t.Fatalf("mappings=%d want 4", len(sum.Mappings))
	}
}

func TestStrictContinuityExitThree(t *testing.T) {
	var buf bytes.Buffer
	write := func(p [188]byte) { buf.Write(p[:]) }
	write(psiPacket(0, 0, patSection(0)))
	write(psiPacket(4096, 0, pmtSection(0, 256)))
	// Audio jumps 5 -> 8: two missing counters.
	for _, cc := range []uint8{0, 1, 2, 3, 4, 5, 8, 9} {
		write(esPacket(257, cc))
	}
	sum, err := New("mem", &buf).Run(true)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.ExitCode != ExitStrictContinuity {
		t.Fatalf("strict exit=%d want 3", sum.ExitCode)
	}
}

func TestMissingPMTIsStructural(t *testing.T) {
	var buf bytes.Buffer
	pat := psiPacket(0, 0, patSection(0)) // PAT references PMT 4096, never sent
	buf.Write(pat[:])
	sum, err := New("mem", &buf).Run(false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.ExitCode != ExitStructural || len(sum.MissingPMTs) != 1 || sum.MissingPMTs[0] != 1 {
		t.Fatalf("missing PMT not structural: exit=%d missing=%v", sum.ExitCode, sum.MissingPMTs)
	}
}

func TestTransportErrorPacketCountedWithoutCCPoison(t *testing.T) {
	var buf bytes.Buffer
	put := func(p [188]byte) { buf.Write(p[:]) }
	put(psiPacket(0, 0, patSection(0)))
	put(psiPacket(4096, 0, pmtSection(0, 256)))
	put(esPacket(257, 0))
	bad := esPacket(257, 1)
	bad[1] |= 0x80 // transport_error_indicator
	put(bad)
	put(esPacket(257, 1)) // same CC as the damaged frame, still legal

	sum, err := New("mem", &buf).Run(false)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if sum.Transport.TransportErrorPackets != 1 {
		t.Fatalf("transport errors=%d, want 1", sum.Transport.TransportErrorPackets)
	}
	var cc *PIDCounters
	for i := range sum.PIDCounters {
		if sum.PIDCounters[i].PID == 257 {
			cc = &sum.PIDCounters[i]
		}
	}
	if cc == nil || cc.ErrorPackets != 1 || cc.ContinuityGaps != 0 {
		t.Fatalf("TEI packet poisoned CC accounting: %+v", cc)
	}
}
