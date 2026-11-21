package ts

import (
	"encoding/binary"
	"testing"
)

// finishSection writes the section_length field, then appends the CRC-32
// computed over the complete section body (which includes that field).
func finishSection(b []byte) []byte {
	// Final length = current body + 4 CRC bytes; section_length excludes
	// the table_id byte and the two length bytes themselves.
	total := len(b) + 4
	binary.BigEndian.PutUint16(b[1:3], uint16(total-3)|0x3000)
	crc := CRC32MPEG2(b)
	return append(b, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func buildPAT(progs []Program) Section {
	// table_id, section_syntax + reserved + section_length(2), transport
	// stream id(2), reserved/version/current, section number, last number.
	b := []byte{tableIDPAT, 0, 0, 0x01, 0x00, 0xc1, 0x00, 0x00}
	for _, p := range progs {
		b = append(b, byte(p.Number>>8), byte(p.Number),
			byte(p.PMTPID>>8)|0xe0, byte(p.PMTPID))
	}
	return parseSectionHeader(finishSection(b))
}

func buildPMT(progNum, pcrPID uint16, streams []ESInfo) Section {
	b := []byte{
		tableIDPMT, 0, 0, // table_id + section_length placeholder
		byte(progNum >> 8), byte(progNum), // program_number
		0xc1, 0x00, 0x00, // version/current + section/last number
		byte(pcrPID>>8) | 0xe0, byte(pcrPID), // PCR_PID
		0xf0, 0x00, // program_info_length = 0
	}
	for _, s := range streams {
		b = append(b, s.StreamType,
			byte(s.EPID>>8)|0xe0, byte(s.EPID), 0xf0, 0x00)
	}
	return parseSectionHeader(finishSection(b))
}

func TestCRC32MPEG2Vector(t *testing.T) {
	// Well-known check value: CRC of the ASCII "123456789".
	if got := CRC32MPEG2([]byte("123456789")); got != 0x0376e6e7 {
		t.Fatalf("crc vector: got %#08x", got)
	}
}

func TestParsePAT(t *testing.T) {
	sec := buildPAT([]Program{
		{Number: 1, PMTPID: 0x100},
		{Number: 2, PMTPID: 0x200},
	})
	progs, err := ParsePAT(sec)
	if err != nil {
		t.Fatal(err)
	}
	if len(progs) != 2 || progs[0].PMTPID != 0x100 || progs[1].Number != 2 {
		t.Fatalf("progs: %+v", progs)
	}
	if sec.Version != 0 || !sec.Current {
		t.Fatalf("header flags: %+v", sec)
	}
}

func TestCRCDetectsCorruption(t *testing.T) {
	sec := buildPAT([]Program{{Number: 1, PMTPID: 0x100}})
	sec.Data[10] ^= 0xff
	if _, err := ParsePAT(sec); err == nil {
		t.Fatal("corrupt PAT must fail CRC")
	}
}

func TestParsePMT(t *testing.T) {
	sec := buildPMT(1, 0x100, []ESInfo{
		{StreamType: StreamTypeH264, EPID: 0x100},
		{StreamType: StreamTypeADTS, EPID: 0x101},
		{StreamType: StreamTypeAAC, EPID: 0x102},
	})
	pmt, err := ParsePMT(sec)
	if err != nil {
		t.Fatal(err)
	}
	if pmt.ProgramNumber != 1 || pmt.PCRPID != 0x100 {
		t.Fatalf("pmt header: %+v", pmt)
	}
	if len(pmt.Streams) != 3 {
		t.Fatalf("streams: %+v", pmt.Streams)
	}
	if pmt.VideoPID() != 0x100 || pmt.AudioPID() != 0x101 {
		t.Fatalf("pids: video=%#x audio=%#x", pmt.VideoPID(), pmt.AudioPID())
	}
}

func TestWrongTableID(t *testing.T) {
	pat := buildPAT(nil)
	if _, err := ParsePMT(pat); err == nil {
		t.Fatal("PAT must not parse as PMT")
	}
	pmt := buildPMT(1, 0x100, nil)
	if _, err := ParsePAT(pmt); err == nil {
		t.Fatal("PMT must not parse as PAT")
	}
}

// packSection wraps a section in 188-byte packets on pid; the first packet
// carries PUSI and a zero pointer field, follow-up packets hold the remainder.
func packSection(pid uint16, data []byte) []Packet {
	var pkts []Packet
	firstCap := PacketSize - 5 // one payload byte used by pointer_field
	cc := uint8(0)

	b := mkPacket(pid, true, cc, 0)
	cc++
	chunk := data
	if len(chunk) > firstCap {
		chunk = data[:firstCap]
	}
	b[4] = 0x00 // pointer_field
	copy(b[5:], chunk)
	p, _ := ParsePacket(b)
	pkts = append(pkts, p)
	rest := data[len(chunk):]

	for len(rest) > 0 {
		n := len(rest)
		if n > PacketSize-4 {
			n = PacketSize - 4
		}
		pb := mkPacket(pid, false, cc&0x0f, 0)
		cc++
		copy(pb[4:4+n], rest[:n])
		pkt, _ := ParsePacket(pb)
		pkts = append(pkts, pkt)
		rest = rest[n:]
	}
	return pkts
}

func TestSectionAssembler(t *testing.T) {
	// 50 programs yield a section larger than the first packet's 183-byte
	// payload capacity (after the pointer field), forcing fragmentation.
	progs := make([]Program, 50)
	for i := range progs {
		progs[i] = Program{Number: uint16(i + 1), PMTPID: uint16(0x100 + i)}
	}
	sec := buildPAT(progs)
	const firstCap = PacketSize - 5
	if len(sec.Data) <= firstCap {
		t.Fatalf("fixture section must span packets: %d", len(sec.Data))
	}
	var a SectionAssembler
	pkts := packSection(PIDPAT, sec.Data)
	if len(pkts) < 2 {
		t.Fatalf("expected multi-packet section, got %d packets", len(pkts))
	}
	var got *Section
	for i, p := range pkts {
		got = a.Push(p)
		if got != nil {
			if i != len(pkts)-1 {
				t.Fatal("section completed before the final fragment")
			}
			break
		}
	}
	if got == nil {
		t.Fatal("section never assembled")
	}
	parsed, err := ParsePAT(*got)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != len(progs) {
		t.Fatalf("programs: got %d, want %d", len(parsed), len(progs))
	}
}
