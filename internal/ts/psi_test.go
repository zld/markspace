package ts

import "testing"

// buildPATSection assembles a minimal single-program PAT with a CRC stub.
func buildPATSection(programNum, pmtPID int) []byte {
	section := make([]byte, 16)
	section[0] = tableIDPAT
	// section_length = 13 (4 bytes after length field + 4 program entry + 4 CRC/end)
	section[1], section[2] = 0xB0, 0x0D
	section[3], section[4] = 0x00, 0x01 // transport stream id
	section[5] = 0xC1                   // version/current-next
	section[6], section[7] = 0x00, 0x00 // section/last section number
	section[8] = byte(programNum >> 8)
	section[9] = byte(programNum)
	section[10] = 0xE0 | byte(pmtPID>>8)
	section[11] = byte(pmtPID)
	return section
}

func TestParsePAT(t *testing.T) {
	section := buildPATSection(0x100, 0x1000)
	num, pid, err := ParsePAT(section)
	if err != nil {
		t.Fatalf("ParsePAT: %v", err)
	}
	if num != 0x100 || pid != 0x1000 {
		t.Fatalf("got program=%d pid=0x%x", num, pid)
	}
}

func TestSectionAssemblerAcrossPackets(t *testing.T) {
	section := buildPATSection(1, 0x0200)
	asm := NewSectionAssembler(PIDPAT)

	// First packet: PUSI + pointer_field 0 + first 8 bytes of section.
	first := makePayload(0, section[:8])
	pkt := &Packet{PID: PIDPAT, PUSI: true, Payload: first}
	out, err := asm.Feed(pkt)
	if err != nil || out != nil {
		t.Fatalf("first feed out=%v err=%v", out, err)
	}
	// Second packet carries the remainder and completes the section.
	rest := &Packet{PID: PIDPAT, Payload: section[8:]}
	out, err = asm.Feed(rest)
	if err != nil {
		t.Fatalf("second feed: %v", err)
	}
	if out == nil || len(out) != len(section) {
		t.Fatalf("section not reassembled: %d bytes", len(out))
	}
}

func TestParsePMTFindsVideoPID(t *testing.T) {
	// PMT header up to program_info_length, then one H.264 ES entry and CRC.
	section := make([]byte, 12+5+4)
	section[0] = tableIDPMT
	section[1], section[2] = 0xB0, byte(len(section)-3) // section_length
	section[3], section[4] = 0x00, 0x01
	section[5] = 0xC1
	section[6], section[7] = 0x00, 0x00
	section[8], section[9] = 0xE0, 0x00 // PCR PID
	section[10], section[11] = 0xF0, 0x00
	section[12] = streamTypeH264
	videoPID := 0x0100
	section[13], section[14] = 0xE0|byte(videoPID>>8), byte(videoPID)
	section[15], section[16] = 0xF0, 0x00 // ES info length 0
	pm, err := ParsePMT(section)
	if err != nil {
		t.Fatalf("ParsePMT: %v", err)
	}
	if pm == nil || pm.VideoPID != videoPID {
		t.Fatalf("video PID = %v, want 0x%x", pm, videoPID)
	}
}

func TestParsePMTRejectsVideoLessStream(t *testing.T) {
	section := make([]byte, 12+5+4)
	section[0] = tableIDPMT
	section[1], section[2] = 0xB0, byte(len(section)-3)
	section[12] = 0x0F // audio only
	if _, err := ParsePMT(section); err == nil {
		t.Fatal("expected error for video-less PMT")
	}
}

// makePayload prefixes a PSI chunk with a pointer_field byte.
func makePayload(pointer int, body []byte) []byte {
	payload := make([]byte, 1+len(body))
	payload[0] = byte(pointer)
	copy(payload[1:], body)
	return payload
}

func TestDecodeTimestamp(t *testing.T) {
	// PTS 0x123456789 encoded per the 40-bit field layout; verify round trip
	// through encode/decode using a hand-built field.
	want := int64(0x123456789)
	b := encodeTimestamp(0b0010, want)
	if got := decodeTimestamp(b); got != want {
		t.Fatalf("decode = 0x%x, want 0x%x", got, want)
	}
}

// encodeTimestamp builds a 5-byte PTS/DTS field for tests.
func encodeTimestamp(prefix byte, pts int64) []byte {
	b := make([]byte, 5)
	b[0] = (prefix << 4) | byte((pts>>29)&0x0E) | 0x01
	b[1] = byte(pts >> 22)
	b[2] = byte((pts>>14)&0xFE) | 0x01
	b[3] = byte(pts >> 7)
	b[4] = byte((pts<<1)&0xFE) | 0x01
	return b
}

func TestIsIDRAccessUnit(t *testing.T) {
	// SPS (7), PPS (8), then IDR slice (5) -> keyframe.
	idr := append(append(AnnexBPrefix, 0x67), 0x11, 0x22)
	idr = append(idr, AnnexBPrefix...)
	idr = append(idr, 0x68, 0x33)
	idr = append(idr, AnnexBPrefix...)
	idr = append(idr, 0x65, 0xAA)
	if !IsIDRAccessUnit(idr) {
		t.Fatal("expected IDR detection")
	}
	// Non-IDR P slice (nal type 1) must not be treated as keyframe.
	pFrame := append(AnnexBPrefix, 0x41, 0xAA)
	if IsIDRAccessUnit(pFrame) {
		t.Fatal("P slice wrongly classified as IDR")
	}
}

func TestSplitAnnexBHandlesFourByteCode(t *testing.T) {
	es := []byte{0, 0, 0, 1, 0x67, 0x11, 0, 0, 0, 1, 0x65, 0x22}
	nals := SplitAnnexB(es)
	if len(nals) != 2 || nals[0][0] != 0x67 || nals[1][0] != 0x65 {
		t.Fatalf("split = %v", nals)
	}
}
