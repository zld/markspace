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

// next returns the next counter value, applying a one-time gap. With
// withGap(after, n) the counter "after" is still emitted normally; the
// following call jumps straight to after+n+1, simulating n lost packets.
func (w *ccWriter) next() uint8 {
	current := w.cc
	if !w.skipped && w.skipAfter >= 0 && int(w.cc) > w.skipAfter {
		// Defensive: once past the gap point without a skip, disable it.
		w.skipped = true
	}
	return current
}

// advance moves to the next counter, inserting the configured skipped values
// immediately after the skipAfter counter has been consumed.
func (w *ccWriter) advance() {
	if !w.skipped && w.skipAfter >= 0 && int(w.cc) == w.skipAfter {
		w.cc = uint8((int(w.cc) + 1 + w.skipCount) & 0x0f)
		w.skipped = true
		return
	}
	w.cc = uint8((int(w.cc) + 1) & 0x0f)
}

// psiPacket wraps one PSI section in a single 188-byte packet with PUSI set.
// The pointer field occupies payload byte 0; remaining bytes are 0xFF stuffed.
func psiPacket(pid uint16, cc uint8, section []byte) [tsreader.PacketSize]byte {
	var pkt [tsreader.PacketSize]byte
	pkt[0] = tsreader.SyncByte
	// PUSI (0x40) plus the 13-bit PID, then payload-only flags and CC.
	pkt[1] = 0x40 | byte(pid>>8)&0x1f
	pkt[2] = byte(pid)
	pkt[3] = 0x10 | (cc & 0x0f)
	pkt[4] = 0x00 // pointer_field, first payload byte
	copy(pkt[5:], section)
	for i := 5 + len(section); i < tsreader.PacketSize; i++ {
		pkt[i] = 0xff
	}
	return pkt
}

// esPacket builds a payload-only elementary stream packet.
func esPacket(pid uint16, cc uint8, tag byte) [tsreader.PacketSize]byte {
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

// setLength writes the 12-bit section_length into section bytes 1-2. The
// value counts every byte after these three, including the 4-byte CRC.
func setLength(section []byte) {
	length := len(section) - 3
	section[1] = (section[1] & 0xf0) | byte(length>>8)
	section[2] = byte(length)
}

// finish appends a CRC-32/MPEG-2 placeholder, fills section_length over the
// complete section, then writes the CRC computed over the final bytes.
func finish(body []byte) []byte {
	section := append(body, 0, 0, 0, 0) // room for CRC
	setLength(section)
	crc := psi.CRC32MPEG2(section[:len(section)-4])
	section[len(section)-4] = byte(crc >> 24)
	section[len(section)-3] = byte(crc >> 16)
	section[len(section)-2] = byte(crc >> 8)
	section[len(section)-1] = byte(crc)
	return section
}

// buildPAT constructs one PAT section for a single program -> PMT PID entry.
func buildPAT(version uint8, program, pmtPID uint16) []byte {
	body := make([]byte, 0, 20)
	body = append(body, psi.TableIDPAT)
	body = append(body, 0xf0, 0x00)             // section_length placeholder
	body = append(body, 0x00, 0x01)             // transport_stream_id
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
	body = append(body, 0xf0, 0x00) // section_length placeholder
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
