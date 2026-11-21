// Package main builds deterministic MPEG-TS fixtures for tsaudit. All tables
// are generated locally; no external media files are required.
package main

import (
	"github.com/example/tsaudit/internal/psi"
	"github.com/example/tsaudit/internal/tsreader"
)

// StreamType values used by the fixtures (kept as raw MPEG-TS identifiers).
const (
	stVideo uint8 = 0x1b // H.264 video
	stAudio uint8 = 0x0f // AAC audio
)

// ccWriter emits packets for one PID while maintaining the 4-bit continuity
// counter. Optionally skipping counters lets fixtures reproduce packet loss.
type ccWriter struct {
	pid   uint16
	cc    uint8
	first bool
	// skipAfter, when >= 0, removes (skipAfter+1 .. skipAfter+skipCount)
	// counters from the sequence once, simulating lost transport packets.
	skipAfter int
	skipCount int
	skipped   bool
}

func newCCWriter(pid uint16) *ccWriter {
	return &ccWriter{pid: pid, cc: 0, first: true, skipAfter: -1}
}

// withGap skips skipCount counter values immediately after counter after.
func (w *ccWriter) withGap(after int, skipCount int) *ccWriter {
	w.skipAfter, w.skipCount = after, skipCount
	return w
}

// next returns the next counter value, applying the one-time gap.
func (w *ccWriter) next() uint8 {
	if !w.skipped && w.skipAfter >= 0 && int(w.cc) == w.skipAfter {
		w.cc = uint8((int(w.cc) + 1 + w.skipCount) & 0x0f)
		w.skipped = true
		return w.cc
	}
	return w.cc
}

// psiPacket wraps one PSI section in a single 188-byte packet with PUSI set.
// The pointer field is zero; remaining bytes are 0xFF stuffed.
func psiPacket(pid uint16, cc uint8, section []byte) [tsreader.PacketSize]byte {
	var pkt [tsreader.PacketSize]byte
	pkt[0] = tsreader.SyncByte
	pkt[1], pkt[2] = headerBytes(pid, true, cc)
	pkt[3] = 0x00 // pointer_field
	copy(pkt[4:], section)
	for i := 4 + len(section); i < tsreader.PacketSize; i++ {
		pkt[i] = 0xff
	}
	return pkt
}

// esPacket builds a payload-only elementary stream packet.
func esPacket(pid uint16, cc uint8, tag byte) [tsreader.PacketSize]byte {
	var pkt [tsreader.PacketSize]byte
	pkt[0] = tsreader.SyncByte
	pkt[1], pkt[2] = headerBytes(pid, false, cc)
	for i := 3; i < tsreader.PacketSize; i++ {
		pkt[i] = tag
	}
	return pkt
}

// headerBytes returns bytes 1-2 of the transport header for a payload packet.
func headerBytes(pid uint16, pusi bool, cc uint8) (byte, byte) {
	b1 := byte(pid >> 8)
	b2 := byte(pid)
	if pusi {
		b1 |= 0x40
	}
	// payload-only: adaptation flags 0b01, plus the 4-bit counter.
	return b1, b2 | 0x10 | (cc & 0x0f)
}

// setLength writes the 12-bit section_length into section bytes 1-2.
func setLength(section []byte) {
	length := len(section) - 3
	section[1] = (section[1] & 0xf0) | byte(length>>8)
	section[2] = byte(length)
}

// finish fills the section_length placeholder and appends a CRC-32/MPEG-2
// computed over the final section bytes.
func finish(section []byte) []byte {
	setLength(section)
	crc := psi.CRC32MPEG2(section)
	return append(section, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

// buildPAT constructs one PAT section for a single program -> PMT PID entry.
func buildPAT(version uint8, program, pmtPID uint16) []byte {
	body := make([]byte, 0, 20)
	body = append(body, psi.TableIDPAT)
	body = append(body, 0xb0, 0x00) // section_length placeholder
	body = append(body, 0x00, 0x01) // transport_stream_id
	body = append(body, 0xc0|(version<<1)|0x01) // current=1
	body = append(body, 0x00, 0x00)             // section/last section number
	body = append(body, byte(program>>8), byte(program))
	body = append(body, 0xe0|byte(pmtPID>>8), byte(pmtPID))
	return finish(body)
}

// buildPMT constructs one single-section PMT for the given stream layout.
func buildPMT(version, program, pcrPID uint16, streams []psi.StreamInfo) []byte {
	body := make([]byte, 0, 12+5*len(streams))
	body = append(body, psi.TableIDPMT)
	body = append(body, 0xb0, 0x00) // section_length placeholder
	body = append(body, byte(program>>8), byte(program))
	body = append(body, 0xc0|byte(version<<1)|0x01)
	body = append(body, 0x00, 0x00)
	body = append(body, 0xe0|byte(pcrPID>>8), byte(pcrPID))
	body = append(body, 0xf0, 0x00) // program_info_length = 0
	for _, s := range streams {
		body = append(body, s.StreamType)
		body = append(body, 0xe0|byte(s.ElementaryPID>>8), byte(s.ElementaryPID))
		body = append(body, 0xf0, 0x00) // ES_info_length = 0
	}
	return finish(body)
}
