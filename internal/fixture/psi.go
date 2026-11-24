package fixture

import (
	ts "mediarun/internal/ts"
)

// Spec configures one generated sample stream.
type Spec struct {
	Kind      string
	Segments  int  // number of 1-second timeline segments
	WrapAtPCR bool // start PCR near the 33-bit boundary (pcr_wrap)
	CCGap     bool // introduce a continuity-counter jump and a duplicate
	PSIUpdate bool // emit a second, higher-version PMT mid-stream
	Truncate  int  // trailing bytes to cut off (0 disables)
	LossyRTP  bool // deliver over RTP with reorder/dup/loss instead of raw TS
	Seed      int64
}

// DefaultSpec returns the standard shape for a scenario.
func DefaultSpec(kind string) Spec {
	s := Spec{Kind: kind, Segments: 6}
	switch kind {
	case KindPCRWrap:
		s.WrapAtPCR = true
	case KindCCJump:
		s.CCGap = true
	case KindPSIUpdate:
		s.PSIUpdate = true
	case KindLoss:
		s.LossyRTP = true
	case KindTruncated:
		s.Truncate = 60
	case KindDamaged:
		// Raw-TS composite: a continuity jump/duplicate plus tail truncation.
		s.CCGap = true
		s.Truncate = 40
	}
	return s
}

// psiSection builds a table_id section with a correct CRC.
func psiSection(tableID byte, body []byte) []byte {
	sectionLen := len(body) + 4
	out := append([]byte{tableID, 0xB0 | byte(sectionLen>>8), byte(sectionLen)}, body...)
	crc := ts.CRC32MPEG2Value(out)
	return append(out, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func pidHiLo(pid uint16) (byte, byte) { return 0xE0 | byte(pid>>8), byte(pid) }

func patSection() []byte {
	hi, lo := pidHiLo(PMTPID)
	body := []byte{
		0x00, 0x01, // tsid
		0xC1, 0x00, 0x00, // version 0, current
		0x00, 0x01, hi, lo, // program 1 -> PMT
	}
	return psiSection(0x00, body)
}

func pmtSection(version uint8) []byte {
	pcrH, pcrL := pidHiLo(PCRPID)
	aH, aL := pidHiLo(AudioPID)
	vH, vL := pidHiLo(VideoPID)
	body := []byte{
		0x00, 0x01, // program number
		0xC1 | (version << 1), 0x00, 0x00,
		pcrH, pcrL, // PCR_PID
		0xF0, 0x00, // program_info_length 0
		0x0F, aH, aL, 0xF0, 0x00, // AAC-ish audio
		0x1B, vH, vL, 0xF0, 0x00, // H.264 video
	}
	return psiSection(0x02, body)
}

// psiPackets wraps PAT/PMT sections as PUSI TS packets.
func (b *tsBuilder) psiPackets(pat, pmt []byte, pmtCC uint8) [][]byte {
	patPkt := rawPacket(PatPID, b.nextCC(PatPID), true, 0x01, nil,
		append([]byte{0x00}, padPayload(pat)...))
	pmtPkt := rawPacket(PMTPID, pmtCC, true, 0x01, nil,
		append([]byte{0x00}, padPayload(pmt)...))
	return [][]byte{patPkt, pmtPkt}
}

func padPayload(p []byte) []byte {
	out := make([]byte, ts.PacketSize-4)
	copy(out, p)
	return out
}

// pcrForSegment returns the PCR_base at the start of a segment. PCR ticks at
// 90 kHz with one segment per second (90000 ticks).
func pcrForSegment(seg int, wrap bool) uint64 {
	if wrap {
		// Anchor two segments before the 33-bit wrap so the wrap occurs mid-stream.
		base := tsMod33 - 2*90000
		return (base + uint64(seg)*90000) % tsMod33
	}
	return uint64(seg) * 90000
}

const tsMod33 uint64 = 1 << 33
