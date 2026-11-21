// Package tsbuild constructs deterministic, spec-valid MPEG-TS packets and
// PAT/PMT sections for the tsaudit fixtures and tests. Everything is produced
// locally from constants; no external media is required.
package tsbuild

import (
	"github.com/example/tsaudit/internal/psi"
	"github.com/example/tsaudit/internal/tsreader"
)

// Raw MPEG-TS stream type identifiers retained as their numeric values.
const (
	StreamTypeVideo uint8 = 0x1b // H.264 video
	StreamTypeAudio uint8 = 0x0f // AAC audio
)

// CCWriter emits continuity-counter values for one PID, optionally skipping a
// range of counters once to reproduce packet loss.
type CCWriter struct {
	pid        uint16
	cc         uint8
	skipAfter  int
	skipCount  int
	skipped    bool
	skipActive bool
}

// NewCCWriter starts a writer at counter zero.
func NewCCWriter(pid uint16) *CCWriter {
	return &CCWriter{pid: pid, skipAfter: -1}
}

// WithGap arranges for n counter values to be skipped immediately after the
// counter "after" has been consumed.
func (w *CCWriter) WithGap(after, n int) *CCWriter {
	w.skipAfter, w.skipCount = after, n
	w.skipActive = true
	return w
}

// Next returns the current counter without advancing.
func (w *CCWriter) Next() uint8 { return w.cc }

// Advance moves to the next counter, inserting the configured gap once.
func (w *CCWriter) Advance() {
	if w.skipActive && !w.skipped && int(w.cc) == w.skipAfter {
		w.cc = uint8((int(w.cc) + 1 + w.skipCount) & 0x0f)
		w.skipped = true
		return
	}
	w.cc = uint8((int(w.cc) + 1) & 0x0f)
}

// PSIPacket wraps one PSI section in a single 188-byte packet with PUSI set.
// The pointer field occupies payload byte 0; remaining bytes are 0xFF stuffed.
func PSIPacket(pid uint16, cc uint8, section []byte) [tsreader.PacketSize]byte {
	var pkt [tsreader.PacketSize]byte
	pkt[0] = tsreader.SyncByte
	pkt[1] = 0x40 | byte(pid>>8)&0x1f // PUSI plus high PID bits
	pkt[2] = byte(pid)
	pkt[3] = 0x10 | (cc & 0x0f) // payload-only flags plus CC
	pkt[4] = 0x00               // pointer_field
	copy(pkt[5:], section)
	for i := 5 + len(section); i < tsreader.PacketSize; i++ {
		pkt[i] = 0xff
	}
	return pkt
}

// ESPacket builds a payload-only elementary stream packet.
func ESPacket(pid uint16, cc uint8, tag byte) [tsreader.PacketSize]byte {
	var pkt [tsreader.PacketSize]byte
	pkt[0] = tsreader.SyncByte
	pkt[1] = byte(pid>>8) & 0x1f
	pkt[2] = byte(pid)
	pkt[3] = 0x10 | (cc & 0x0f)
	for i := 4; i < tsreader.PacketSize; i++ {
		pkt[i] = tag
	}
	return pkt
}

// finish appends CRC room, fills section_length over the complete section and
// writes a CRC-32/MPEG-2 computed over the final pre-CRC bytes.
func finish(body []byte) []byte {
	section := append(body, 0, 0, 0, 0) // room for CRC
	length := len(section) - 3
	section[1] = (section[1] & 0xf0) | byte(length>>8)
	section[2] = byte(length)
	crc := psi.CRC32MPEG2(section[:len(section)-4])
	section[len(section)-4] = byte(crc >> 24)
	section[len(section)-3] = byte(crc >> 16)
	section[len(section)-2] = byte(crc >> 8)
	section[len(section)-1] = byte(crc)
	return section
}

// BuildPAT constructs one PAT section mapping a single program to its PMT PID.
func BuildPAT(version uint8, program, pmtPID uint16) []byte {
	body := []byte{
		psi.TableIDPAT,
		0xf0, 0x00, // section_length placeholder
		0x00, 0x01, // transport_stream_id
		0xc0 | (version << 1) | 0x01, // current_next = 1
		0x00, 0x00,                   // section_number, last_section_number
		byte(program >> 8), byte(program),
		0xe0 | byte(pmtPID>>8), byte(pmtPID),
	}
	return finish(body)
}

// BuildPMT constructs one single-section PMT for the given stream layout.
func BuildPMT(version, program, pcrPID uint16, streams []psi.StreamInfo) []byte {
	body := []byte{
		psi.TableIDPMT,
		0xf0, 0x00, // section_length placeholder
		byte(program >> 8), byte(program),
		0xc0 | byte(version<<1) | 0x01,
		0x00, 0x00,
		0xe0 | byte(pcrPID>>8), byte(pcrPID),
		0xf0, 0x00, // program_info_length = 0
	}
	for _, s := range streams {
		body = append(body, s.StreamType,
			0xe0|byte(s.ElementaryPID>>8), byte(s.ElementaryPID),
			0xf0, 0x00) // ES_info_length = 0
	}
	return finish(body)
}
