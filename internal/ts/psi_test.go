package ts

import (
	"testing"

	"mediarun/internal/clock"
)

// buildSection assembles a syntactically valid PSI section with CRC_32.
// body holds everything after the section_length field; the length field
// itself (len(body)+4) and the table_id are prepended here.
func buildSection(tableID byte, body []byte) []byte {
	sectionLen := len(body) + 4
	out := []byte{tableID, byte(0xB0 | (sectionLen >> 8)), byte(sectionLen)}
	out = append(out, body...)
	crc := computeCRC(out)
	return append(out, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func computeCRC(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc = (crc << 8) ^ crc32MPEG2Table[byte(crc>>24)^b]
	}
	return crc
}

// psiPacket wraps one section in a single TS packet payload with pointer_field 0.
func psiPacket(pid uint16, cc uint8, section []byte) *Packet {
	payload := append([]byte{0x00}, section...)
	return &Packet{PID: pid, CC: cc, HasPayload: true, PayloadUnitStart: true,
		Payload: payload, Offset: int64(cc) * 188}
}

// body holds bytes after section_length. In the complete section the version
// byte lands at offset 5, program loop at offset 8, CRC in the last 4 bytes.
// pidWords encodes a 13-bit PID as (high, low): the high word keeps its 5
// bits and sets 111 reserved bits; the low word is the PID's full low byte.
func pidWords(pid uint16) (hi, lo byte) {
	return 0xE0 | byte(pid>>8), byte(pid)
}

func patSection(version uint8, pmtPID uint16) []byte {
	hi, lo := pidWords(pmtPID)
	body := []byte{
		0x00, 0x01, // transport_stream_id (bytes 3,4)
		0xC1 | (version << 1), // byte 5: version/control
		0x00, 0x00,            // section_number, last_section_number (6,7)
		0x00, 0x01, hi, lo, // bytes 8-11: program 1 -> PMT PID
	}
	return buildSection(0x00, body)
}

func pmtSection(version uint8, pmtPID, audioPID, videoPID uint16, audioType byte) []byte {
	ph, pl := pidWords(pmtPID)
	ah, al := pidWords(audioPID)
	vh, vl := pidWords(videoPID)
	body := []byte{
		0x00, 0x01, // program number (bytes 3,4)
		0xC1 | (version << 1), // byte 5: version/control
		0x00, 0x00,            // section_number, last_section_number (6,7)
		ph, pl, // PCR_PID (bytes 8,9)
		0xF0, 0x00, // program_info_length = 0 (bytes 10,11)
		audioType, ah, al, 0xF0, 0x00,
		0x1B, vh, vl, 0xF0, 0x00,
	}
	return buildSection(0x02, body)
}

func feedPSI(t *testing.T, rec *clock.Recorder, versions ...uint8) *PSITracker {
	t.Helper()
	tr := NewPSITracker()
	cc := uint8(0)
	tr.Observe(psiPacket(0x0000, cc, patSection(0, 0x1000)), rec)
	cc++
	for i, v := range versions {
		audioPID := uint16(0x0100 + i*0x10)
		videoPID := uint16(0x0200 + i*0x10)
		tr.Observe(psiPacket(0x1000, cc, pmtSection(v, 0x1000, audioPID, videoPID, 0x0F)), rec)
		cc++
	}
	return tr
}

func TestPSI_FullMappingAndClassification(t *testing.T) {
	rec := &clock.Recorder{}
	tr := feedPSI(t, rec, 0)
	m := tr.Current()
	if m.PMTPID != 0x1000 || m.AudioPID != 0x0100 || m.VideoPID != 0x0200 {
		t.Fatalf("mapping wrong: %+v", m)
	}
	if tr.TrackOf(0x0100) != "audio" || tr.TrackOf(0x0200) != "video" {
		t.Fatal("stream classification wrong")
	}
	if tr.Epoch() != 1 {
		t.Fatalf("epoch = %d, want 1", tr.Epoch())
	}
}

func TestPSI_VersionUpdateKeepsOldEpochIsolated(t *testing.T) {
	rec := &clock.Recorder{}
	tr := feedPSI(t, rec, 0, 1) // v0 then v1: audio PID moves 0x100 -> 0x110
	m := tr.Current()
	if m.Version != 1 || m.AudioPID != 0x0110 || tr.Epoch() != 2 {
		t.Fatalf("update not applied: %+v epoch=%d", m, tr.Epoch())
	}
	// Old PID must not be misread as the new audio track.
	if tr.TrackOf(0x0100) != "unknown" {
		t.Fatal("old-version PID leaked into the new mapping")
	}
	if tr.TrackOf(0x0110) != "audio" {
		t.Fatal("new audio PID not mapped")
	}
	hist := tr.History()
	if len(hist) != 1 || hist[0].AudioPID != 0x0100 || hist[0].Streams[0x0100].Epoch != 1 {
		t.Fatalf("old mapping not preserved: %+v", hist)
	}
	if m.Streams[0x0110].Epoch != 2 {
		t.Fatal("new stream must carry the new epoch")
	}
}

func TestPSI_SameVersionIsNoop(t *testing.T) {
	rec := &clock.Recorder{}
	tr := NewPSITracker()
	sec := patSection(3, 0x1000)
	tr.Observe(psiPacket(0, 0, sec), rec)
	tr.Observe(psiPacket(0, 1, sec), rec)
	if got := rec.CountByKind()["psi_update"]; got != 1 {
		t.Fatalf("repeated version produced %d updates, want 1", got)
	}
}

func TestPSI_BadCRCRejected(t *testing.T) {
	rec := &clock.Recorder{}
	tr := NewPSITracker()
	sec := patSection(0, 0x1000)
	sec[9] ^= 0xFF // corrupt body, CRC now invalid
	tr.Observe(psiPacket(0, 0, sec), rec)
	if rec.CountByKind()["psi_crc"] != 1 || tr.Current().PMTPID != 0 {
		t.Fatalf("corrupt section must be rejected: events=%v map=%+v", rec.Events(), tr.Current())
	}
}
