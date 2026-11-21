package psi

import "testing"

// buildTestSection assembles a spec-valid section with a correct CRC.
func buildTestSection(tableID uint8, ext uint16, version uint8, loop []byte) []byte {
	s := []byte{
		tableID, 0xf0, 0x00, // section_length placeholder
		byte(ext >> 8), byte(ext),
		0xc1 | (version << 1), // current=1, fixed low bit
		0x00, 0x00,            // section_number, last_section_number
	}
	s = append(s, loop...)
	length := len(s) - 3 + 4 // include the CRC that follows
	s[1] = (s[1] & 0xf0) | byte(length>>8)
	s[2] = byte(length)
	crc := crc32MPEG2(s)
	return append(s, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func TestCRC32KnownVector(t *testing.T) {
	if got := crc32MPEG2([]byte("123456789")); got != 0x0376e6e7 {
		t.Fatalf("CRC32/MPEG-2 = %08x, want 0376e6e7", got)
	}
}

func TestValidateSectionAcceptsAndRejects(t *testing.T) {
	// PAT with one program 1 -> PMT PID 0x1000.
	loop := []byte{0x00, 0x01, 0xe0 | 0x10, 0x00}
	sec := buildTestSection(TableIDPAT, 0x0001, 0, loop)
	if _, err := ValidateSection(sec); err != nil {
		t.Fatalf("valid section rejected: %v", err)
	}

	bad := append([]byte(nil), sec...)
	bad[8] ^= 0xff // corrupt a loop byte
	if _, err := ValidateSection(bad); err == nil {
		t.Fatal("CRC-corrupt section accepted")
	}

	short := []byte{TableIDPAT, 0xf0, 0x05}
	if _, err := ValidateSection(short); err == nil {
		t.Fatal("undersized section accepted")
	}
}

func TestAssemblerPublishesPATOnceAndCountsRetransmissions(t *testing.T) {
	a := NewAssembler()
	loop := []byte{0x00, 0x01, 0xe0 | 0x10, 0x00}
	sec := buildTestSection(TableIDPAT, 0x0001, 0, loop)

	a.FeedPayload(PIDPAT, true, sec)
	a.FeedPayload(PIDPAT, true, sec) // identical retransmission
	if len(a.Publications) != 1 {
		t.Fatalf("publications=%d, want 1", len(a.Publications))
	}
	st := a.Stats()[0]
	if st.Retransmissions != 1 || st.VersionChanges != 0 || st.CurrentVersion != 0 {
		t.Fatalf("retransmit stats wrong: %+v", st)
	}
	pat := a.Publications[0].PAT
	if pat == nil || pat.Programs[1] != 0x1000 {
		t.Fatalf("PAT program mapping wrong: %+v", pat)
	}
}

func TestAssemblerVersionChangeAndWrap(t *testing.T) {
	a := NewAssembler()
	loop := []byte{0x00, 0x01, 0xe0 | 0x10, 0x00}
	a.FeedPayload(PIDPAT, true, buildTestSection(TableIDPAT, 1, 0, loop))
	a.FeedPayload(PIDPAT, true, buildTestSection(TableIDPAT, 1, 1, loop))
	a.FeedPayload(PIDPAT, true, buildTestSection(TableIDPAT, 1, 31, loop))
	a.FeedPayload(PIDPAT, true, buildTestSection(TableIDPAT, 1, 0, loop)) // wrap

	if len(a.Publications) != 4 {
		t.Fatalf("publications=%d, want 4", len(a.Publications))
	}
	st := a.Stats()[0]
	if st.VersionChanges != 3 || st.Wraps != 1 {
		t.Fatalf("version stats wrong: %+v", st)
	}
	if !a.Publications[3].Wrapped {
		t.Fatal("31 -> 0 transition not flagged as wrap")
	}
}

func TestCorruptSectionDoesNotPublish(t *testing.T) {
	a := NewAssembler()
	// Minimal legal PMT body after the common header: PCR_PID, zero
	// program_info_length, then one elementary stream entry.
	loop := []byte{
		0xe1, 0x00, // PCR_PID 0x100
		0xf0, 0x00, // program_info_length = 0
		0x1b, 0xe1, 0x00, // stream type video, elementary PID 0x100
		0xf0, 0x00, // ES_info_length = 0
	}
	good := buildTestSection(TableIDPMT, 1, 0, loop)
	bad := buildTestSection(TableIDPMT, 1, 2, loop)
	bad[8] ^= 0xff

	a.FeedPayload(4096, true, good)
	a.FeedPayload(4096, true, bad)

	if len(a.Publications) != 1 || a.Publications[0].Version != 0 {
		t.Fatalf("corrupt v2 must not publish: %+v", a.Publications)
	}
	if len(a.Corrupt) != 1 || a.Corrupt[0].Version != 2 {
		t.Fatalf("corrupt candidate not recorded: %+v", a.Corrupt)
	}
	// The good mapping survives and the bad version never replaced it.
	pmt := a.Publications[0].PMT
	if pmt == nil || len(pmt.Streams) != 1 || pmt.Streams[0].ElementaryPID != 0x100 {
		t.Fatalf("PMT content wrong: %+v", pmt)
	}
}

func TestCrossPacketSectionReassembly(t *testing.T) {
	a := NewAssembler()
	loop := []byte{0x00, 0x01, 0xe0 | 0x10, 0x00}
	sec := buildTestSection(TableIDPAT, 1, 0, loop)

	// Split the section across a start packet and a continuation packet.
	a.FeedPayload(PIDPAT, true, sec[:7])
	if len(a.Publications) != 0 {
		t.Fatal("partial section published early")
	}
	a.FeedPayload(PIDPAT, false, sec[7:])
	if len(a.Publications) != 1 {
		t.Fatalf("cross-packet section not published, corrupt=%+v", a.Corrupt)
	}
}
