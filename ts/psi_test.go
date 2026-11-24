package ts

import (
	"encoding/binary"
	"testing"
)

// psiSection wraps a table body with pointer byte, header and real CRC.
// tableIDExt is the table id extension (program_number for PAT/PMT).
func psiSection(tableID uint8, tableIDExt uint16, body []byte, version uint8) []byte {
	// section_length covers the 5 header/version bytes, the body and 4 CRC bytes.
	sectionLen := uint16(5 + len(body) + 4)
	head := []byte{
		tableID,
		0xb0 | byte(sectionLen>>8), // section_syntax=1, length high bits
		byte(sectionLen & 0xff),
		byte(tableIDExt >> 8), byte(tableIDExt),
		0xc0 | version<<1 | 0x01, // current/next=1
		0x00, 0x00,               // section number / last section number
	}
	section := append(head, body...)
	crc := CRC32MPEG2(section)
	crcB := make([]byte, 4)
	binary.BigEndian.PutUint32(crcB, crc)
	return append([]byte{0x00}, append(section, crcB...)...) // pointer field = 0
}

func patSection(program, pmtPID uint16, version uint8) []byte {
	body := make([]byte, 4)
	binary.BigEndian.PutUint16(body[0:], program)
	binary.BigEndian.PutUint16(body[2:], 0xe000|pmtPID)
	return psiSection(0x00, 0x0001, body, version)
}

func pmtSection(program, pcrPID, esPID uint16, streamType uint8, version uint8) []byte {
	body := []byte{
		0xe0 | byte(pcrPID>>8), byte(pcrPID), // PCR PID
		0xf0, 0x00, // program info length 0
		streamType,
		0xe0 | byte(esPID>>8), byte(esPID),
		0xf0, 0x00, // ES info length 0
	}
	return psiSection(0x02, program, body, version)
}

func tablePacket(pid uint16, pusi bool, cc uint8, payload []byte) []byte {
	b := make([]byte, PacketSize)
	b[0] = SyncByte
	b[1] = byte(pid >> 8)
	b[2] = byte(pid)
	b[3] = 0x10 | cc
	if pusi {
		b[1] |= 0x40
	}
	copy(b[4:], payload)
	return b
}

func TestPATPMTHappyPath(t *testing.T) {
	tr := NewPSITracker()
	pat := tablePacket(PIDPAT, true, 0, patSection(1, 0x1000, 1))
	pkt, _ := Parse(pat, 0)
	changed, err := tr.Feed(pkt)
	if err != nil || !changed {
		t.Fatalf("PAT apply: changed=%v err=%v", changed, err)
	}
	if tr.Current().Programs[1].PMTPID != 0x1000 {
		t.Fatalf("PAT entry wrong: %+v", tr.Current().Programs)
	}
	pmt := tablePacket(0x1000, true, 0, pmtSection(1, 0x0101, 0x0101, 0x1b, 1))
	pkt, _ = Parse(pmt, PacketSize)
	if changed, err = tr.Feed(pkt); err != nil || !changed {
		t.Fatalf("PMT apply: changed=%v err=%v", changed, err)
	}
	streams := tr.Current().Streams[1]
	if len(streams) != 1 || streams[0].PID != 0x0101 || streams[0].Type != 0x1b {
		t.Fatalf("PMT streams wrong: %+v", streams)
	}
	if len(tr.Current().EffectivePIDs) != 1 || tr.Current().EffectivePIDs[0] != 0x0101 {
		t.Fatalf("effective PIDs wrong: %v", tr.Current().EffectivePIDs)
	}
}

func TestPSIBadCRCRejected(t *testing.T) {
	tr := NewPSITracker()
	section := patSection(1, 0x1000, 1)
	section[len(section)-1] ^= 0xff // corrupt CRC byte
	pkt, _ := Parse(tablePacket(PIDPAT, true, 0, section), 0)
	changed, err := tr.Feed(pkt)
	if err != nil || changed {
		t.Fatalf("corrupt section must not apply: changed=%v err=%v", changed, err)
	}
	if len(tr.Errs) != 1 || tr.Errs[0].Kind != "crc" {
		t.Fatalf("want one crc error, got %+v", tr.Errs)
	}
	if len(tr.Current().Programs) != 0 {
		t.Fatal("mapping must stay empty after CRC failure")
	}
}

func TestPSIOldVersionDoesNotOverride(t *testing.T) {
	tr := NewPSITracker()
	feed := func(section []byte, pid uint16, offset int64) {
		pkt, _ := Parse(tablePacket(pid, true, 0, section), offset)
		if _, err := tr.Feed(pkt); err != nil {
			t.Fatal(err)
		}
	}
	feed(patSection(1, 0x1000, 3), PIDPAT, 0)
	feed(patSection(1, 0x0200, 2), PIDPAT, PacketSize) // older version
	if tr.Current().Programs[1].PMTPID != 0x1000 {
		t.Fatalf("old version overrode newer map: %+v", tr.Current().Programs)
	}
	if len(tr.Errs) != 1 || tr.Errs[0].Kind != "psi_version" {
		t.Fatalf("want psi_version error, got %+v", tr.Errs)
	}
	// A newer version with valid CRC does take effect.
	feed(patSection(1, 0x0200, 4), PIDPAT, 2*PacketSize)
	if tr.Current().Programs[1].PMTPID != 0x0200 {
		t.Fatalf("newer version must apply: %+v", tr.Current().Programs)
	}
}

func TestPSIMultiPacketSection(t *testing.T) {
	tr := NewPSITracker()
	section := patSection(7, 0x0777, 1)
	// Split inside the section (after the 3-byte length header). The PUSI
	// packet carries pointer + the 3 header bytes; the continuation packet
	// carries the remaining section bytes. Both are full 184-byte packets,
	// but only the written prefix of each payload is meaningful.
	split := 1 + 3
	first := tablePacket(PIDPAT, true, 0, section[:split])
	second := tablePacket(PIDPAT, false, 1, section[split:])
	p1, _ := Parse(first, 0)
	p2, _ := Parse(second, PacketSize)
	// Simulate a generator that writes only the meaningful prefix: shrink
	// the parsed payloads so stuffing is not mistaken for section bytes.
	p1.Payload = p1.Payload[:split]
	p2.Payload = p2.Payload[:len(section)-split]
	if _, err := tr.Feed(p1); err != nil {
		t.Fatal(err)
	}
	changed, err := tr.Feed(p2)
	if err != nil || !changed {
		t.Fatalf("reassembled section must apply: changed=%v err=%v", changed, err)
	}
	if tr.Current().Programs[7].PMTPID != 0x0777 {
		t.Fatalf("reassembled PAT wrong: %+v", tr.Current().Programs)
	}
}

func TestCRC32KnownVector(t *testing.T) {
	// The checksum of "123456789" under MPEG2 CRC is 0x0376E6E7.
	if got := CRC32MPEG2([]byte("123456789")); got != 0x0376e6e7 {
		t.Fatalf("CRC32MPEG2 vector = %08x, want 0376e6e7", got)
	}
}
