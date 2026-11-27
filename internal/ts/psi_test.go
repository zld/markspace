package ts

import (
	"testing"

	"tsrepair/internal/mediaerr"
)

// buildPATSection constructs a CRC-valid single-section PAT for one program.
func buildPATSection(version uint8, program, pmtPID uint16) []byte {
	// section_length = 13: 5-byte table header + 4-byte program row + 4 CRC.
	body := []byte{
		TableIDPAT,
		0xB0, 0x0D,
		byte(program >> 8), byte(program),
		0xC1 | (version << 1), // version 5 bits, current/next=1
		0x00, 0x00,            // section_number=0, last_section_number=0
		byte(program >> 8), byte(program),
		0xE0 | byte(pmtPID>>8), byte(pmtPID),
		0x00, 0x00, 0x00, 0x00, // CRC placeholder
	}
	return appendCRC(body)
}

// buildPMTSection constructs a CRC-valid single-section PMT.
func buildPMTSection(version, program, pcrPID uint16, streams []StreamEntry) []byte {
	body := []byte{
		TableIDPMT,
		0xB0, 0x00, // section_length placeholder
		byte(program >> 8), byte(program),
		0xC1 | byte(version<<1),
		0x00, 0x00,
		0xE0 | byte(pcrPID>>8), byte(pcrPID),
		0xF0, 0x00, // program_info_length=0
	}
	for _, s := range streams {
		body = append(body, s.StreamType, 0xE0|byte(s.PID>>8), byte(s.PID), 0xF0, 0x00)
	}
	body = append(body, 0x00, 0x00, 0x00, 0x00) // CRC placeholder
	sectionLen := len(body) - 3
	body[1] = 0xB0 | byte(sectionLen>>8)
	body[2] = byte(sectionLen)
	return appendCRC(body)
}

// appendCRC replaces the trailing 4 zero bytes with the MPEG-2 CRC so that
// crc32MPEG2 over the whole section yields residue zero.
func appendCRC(section []byte) []byte {
	crc := crc32MPEG2(section[:len(section)-4])
	section[len(section)-4] = byte(crc >> 24)
	section[len(section)-3] = byte(crc >> 16)
	section[len(section)-2] = byte(crc >> 8)
	section[len(section)-1] = byte(crc)
	return section
}

func psiPacket(pid uint16, pusi bool, cc uint8, payload []byte) *Packet {
	p := &Packet{
		PID: pid, PUSI: pusi, CC: cc,
		AdaptationControl: AFCPayloadOnly, AdaptationLength: -1,
		Offset: int64(cc) * PacketSize,
		Payload:           payload,
	}
	return p
}

// psiStartPacket wraps a section in a PUSI payload preceded by a zero
// pointer_field.
func psiStartPacket(pid uint16, cc uint8, section []byte) *Packet {
	return psiPacket(pid, true, cc, append([]byte{0x00}, section...))
}

func TestPATPMTLifecycle(t *testing.T) {
	ec := &mediaerr.Collector{}
	tr := NewPSITracker(ec)
	pat := buildPATSection(1, 42, 0x1000)
	pmt := buildPMTSection(1, 42, 0x0100, []StreamEntry{
		{StreamType: 0x1B, PID: 0x0100}, // H.264 video
		{StreamType: 0x0F, PID: 0x0101}, // AAC audio
	})

	tr.HandlePacket(psiStartPacket(PIDPAT, 0, pat))
	if len(tr.Active().Programs) != 1 || tr.Active().Programs[0].PMTPID != 0x1000 {
		t.Fatalf("PAT not committed: %+v", tr.Active().Programs)
	}
	tr.HandlePacket(psiStartPacket(0x1000, 0, pmt))
	got := tr.Active().PMTs[0x1000]
	if got == nil || got.PCRPID != 0x0100 || len(got.Streams) != 2 {
		t.Fatalf("PMT not committed: %+v", got)
	}
	if len(ec.All()) != 0 {
		t.Errorf("unexpected errors: %v", ec.All())
	}
}

func TestPATVersionRollbackKeepsMapping(t *testing.T) {
	ec := &mediaerr.Collector{}
	tr := NewPSITracker(ec)
	tr.HandlePacket(psiStartPacket(PIDPAT, 0, buildPATSection(5, 1, 0x1000)))
	tr.HandlePacket(psiStartPacket(PIDPAT, 1, buildPATSection(3, 2, 0x2000)))
	if ec.CountByCode(mediaerr.CodeVersionRollback) != 1 {
		t.Fatalf("errors: %v", ec.All())
	}
	if tr.Active().Programs[0].ProgramNumber != 1 {
		t.Error("rollback must preserve previous mapping")
	}
}

func TestPATCRCErrorKeepsMapping(t *testing.T) {
	ec := &mediaerr.Collector{}
	tr := NewPSITracker(ec)
	good := buildPATSection(1, 1, 0x1000)
	tr.HandlePacket(psiStartPacket(PIDPAT, 0, good))
	bad := buildPATSection(2, 9, 0x2000)
	bad[8] ^= 0xFF // corrupt a payload byte after the CRC was computed
	tr.HandlePacket(psiStartPacket(PIDPAT, 1, bad))
	if ec.CountByCode(mediaerr.CodeCRCError) != 1 {
		t.Fatalf("errors: %v", ec.All())
	}
	if tr.Active().Programs[0].ProgramNumber != 1 {
		t.Error("CRC-bad version must not replace the mapping")
	}
}

func TestPATMissingSection(t *testing.T) {
	ec := &mediaerr.Collector{}
	tr := NewPSITracker(ec)
	section := buildPATSection(1, 1, 0x1000)
	// Claim last_section_number=1 while only section 0 arrives.
	section[7] = 1
	section = appendCRC(section)
	tr.HandlePacket(psiStartPacket(PIDPAT, 0, section))
	if ec.CountByCode(mediaerr.CodeSectionMissing) == 0 {
		t.Fatalf("errors: %v", ec.All())
	}
	if len(tr.Active().Programs) != 0 {
		t.Error("incomplete version must not become active")
	}
}

func TestOversizedSectionRejected(t *testing.T) {
	ec := &mediaerr.Collector{}
	asm := &sectionAssembler{pid: PIDPAT}
	pkt := psiStartPacket(PIDPAT, 0, []byte{0x00, 0x0F, 0xFC}) // length 4092
	asm.push(pkt, ec)
	if ec.CountByCode(mediaerr.CodeSectionTooLong) != 1 {
		t.Fatalf("errors: %v", ec.All())
	}
}

func TestSectionAssembledAcrossPackets(t *testing.T) {
	ec := &mediaerr.Collector{}
	asm := &sectionAssembler{pid: PIDPAT}
	section := buildPATSection(1, 7, 0x1100)
	full := append([]byte{0x00}, section...) // with pointer field
	split := 9
	first := psiPacket(PIDPAT, true, 0, full[:split])
	rest := psiPacket(PIDPAT, false, 1, full[split:])
	out := asm.push(first, ec)
	if len(out) != 0 {
		t.Fatal("section must not complete early")
	}
	out = asm.push(rest, ec)
	if len(out) != 1 || len(out[0]) != len(section) {
		t.Fatalf("expected one complete section of %d bytes, got %d", len(section), len(out))
	}
}
