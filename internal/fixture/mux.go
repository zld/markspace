// Package fixture synthesizes deterministic, self-contained H.264/AAC
// MPEG-TS sample streams and the fault scenarios used by the demo and the
// regression tests. No external media files are referenced.
package fixture

import (
	"bytes"

	"hlsflow/internal/ts"
)

// Fixed PIDs and identity used by every generated stream.
const (
	patPID   = 0x0000
	pmtPID   = 0x1000
	videoPID = 0x0100
	audioPID = 0x0101
)

// Muxer accumulates 188-byte TS packets with correct continuity counters and
// periodic PAT/PMT tables. It is deliberately minimal but produces streams
// that the ts package parses through its normal code path.
type Muxer struct {
	buf      bytes.Buffer
	cc       map[uint16]uint8
	pat      []byte
	pmt      []byte
	psiCount int
}

// NewMuxer builds a muxer with one H.264 video and one AAC audio elementary
// stream.
func NewMuxer() *Muxer {
	m := &Muxer{cc: map[uint16]uint8{}}
	m.pat = m.buildPAT()
	m.pmt = m.buildPMT()
	m.emitPSI()
	return m
}

func (m *Muxer) buildPAT() []byte {
	// pointer(1) + header(8) + program entry(4) + CRC(4)
	s := make([]byte, 17)
	s[1] = 0x00
	s[2], s[3] = 0xB0, 0x0D // section_length 13
	s[4], s[5] = 0x00, 0x01
	s[6] = 0xC1
	s[7], s[8] = 0x00, 0x00
	s[9], s[10] = 0x00, 0x01
	s[11], s[12] = 0xF0, 0x00 // PMT PID 0x1000
	return s
}

func (m *Muxer) buildPMT() []byte {
	// pointer(1) + header(8) + pcr/info(4) + 2 ES entries(10) + CRC(4)
	s := make([]byte, 27)
	s[1] = 0x02
	s[2], s[3] = 0xB0, 0x17 // section_length 23
	s[4], s[5] = 0x00, 0x01
	s[6] = 0xC1
	s[7], s[8] = 0x00, 0x00
	s[9], s[10] = 0xF1, 0x00 // PCR PID 0x1000
	s[11], s[12] = 0xF0, 0x00
	s[13] = ts.StreamH264
	s[14], s[15] = 0x01, 0x00
	s[16], s[17] = 0xF0, 0x00
	s[18] = ts.StreamAACADTS
	s[19], s[20] = 0x01, 0x01
	s[21], s[22] = 0xF0, 0x00
	return s
}

func (m *Muxer) emitPSI() {
	m.writePayload(patPID, true, m.pat)
	m.writePayload(pmtPID, true, m.pmt)
}

// writePacket emits one raw packet and bumps the per-PID continuity counter.
func (m *Muxer) writePacket(pid uint16, pusi bool, payload []byte, afc byte) {
	b := make([]byte, ts.PacketSize)
	b[0] = 0x47
	if pusi {
		b[1] |= 0x40
	}
	b[1] |= byte(pid >> 8)
	b[2] = byte(pid)
	b[3] = (afc << 4) | m.cc[pid]
	if afc == ts.AFCPayload || afc == ts.AFCBoth {
		copy(b[4:], payload)
	}
	m.buf.Write(b)
	m.cc[pid] = (m.cc[pid] + 1) & 0x0F
}

// writePayload fragments payload into payload-only packets, the first marked
// with the payload unit start indicator.
func (m *Muxer) writePayload(pid uint16, pusi bool, payload []byte) {
	first := true
	for len(payload) > 0 {
		chunk := payload
		if len(chunk) > ts.PacketSize-4 {
			chunk = chunk[:ts.PacketSize-4]
		}
		m.writePacket(pid, pusi && first, chunk, ts.AFCPayload)
		payload = payload[len(chunk):]
		first = false
	}
}

// maybePSI re-emits PAT/PMT periodically like a real encoder.
func (m *Muxer) maybePSI() {
	m.psiCount++
	if m.psiCount%25 == 0 {
		m.emitPSI()
	}
}

// Bytes returns the assembled transport stream.
func (m *Muxer) Bytes() []byte { return m.buf.Bytes() }
