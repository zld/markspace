// Package fixture synthesizes self-contained RTP/MPEG-TS regression traces.
// Every byte is generated locally in Go (audio via the local ffmpeg); no
// external sample files are referenced. Builders expose knobs for the exact
// fault classes the regression tool must detect and survive.
package fixture

import (
	"bytes"
)

// Fixed identifiers for the single-program synthetic stream.
const (
	PatPID   uint16 = 0x0000
	PmtPID   uint16 = 0x1000
	AudioPID uint16 = 0x0100
	Program  uint16 = 1
	AACType  uint8  = 0x0F // ADTS AAC
)

const tsPacketSize = 188

// TSBuilder emits spec-valid 188-byte TS packets with explicit control over
// PCR values, PSI versions and continuity counters.
type TSBuilder struct {
	packets [][]byte
	cc      map[uint16]uint8
	mark    int // index of the first packet not yet drained
}

// NewTSBuilder starts an empty builder.
func NewTSBuilder() *TSBuilder {
	return &TSBuilder{cc: map[uint16]uint8{}}
}

// Drain returns packets added since the previous drain and advances the mark.
func (b *TSBuilder) Drain() [][]byte {
	out := b.packets[b.mark:]
	b.mark = len(b.packets)
	return out
}

// tailPackets is an alias used by the orchestrator after each media round.
func (b *TSBuilder) tailPackets() [][]byte { return b.Drain() }

func (b *TSBuilder) emit(pkt []byte) []byte {
	b.packets = append(b.packets, pkt)
	return pkt
}

func (b *TSBuilder) nextCC(pid uint16) uint8 {
	v := b.cc[pid]
	b.cc[pid] = (v + 1) & 0x0F
	return v
}

// forceCC overrides the auto continuity counter, used to inject CC jumps.
func (b *TSBuilder) forceCC(pid uint16, cc uint8) { b.cc[pid] = cc }

func header(pid uint16, pusi, af, payload bool, cc uint8) []byte {
	pkt := make([]byte, tsPacketSize)
	pkt[0] = 0x47
	if pusi {
		pkt[1] |= 0x40
	}
	pkt[1] |= byte(pid>>8) & 0x1F
	pkt[2] = byte(pid)
	if af {
		pkt[3] |= 0x20
	}
	if payload {
		pkt[3] |= 0x10
	}
	pkt[3] |= cc & 0x0F
	return pkt
}

// AddPAT emits one PAT section referencing the fixed PMT PID.
func (b *TSBuilder) AddPAT(version uint8) []byte {
	loop := []byte{
		byte(Program >> 8), byte(Program),
		byte(0xE0 | (PmtPID >> 8)), byte(PmtPID & 0xFF),
	}
	sec := psiSection(0x00, 1, version, loop)
	pkt := header(PatPID, true, false, true, b.nextCC(PatPID))
	copy(pkt[4:], append([]byte{0x00}, sec...))
	return b.emit(pkt)
}

// AddPATPacket is an explicit-name alias for mid-stream retransmits.
func (b *TSBuilder) AddPATPacket(version uint8) []byte { return b.AddPAT(version) }

// AddPMT emits one PMT section with the fixed AAC audio elementary stream.
func (b *TSBuilder) AddPMT(version uint8) []byte {
	es := []byte{
		AACType,
		byte(0xE0 | (AudioPID >> 8)), byte(AudioPID & 0xFF),
		0xF0, 0x00, // ES info length 0
	}
	inner := 9 + len(es) + 4
	sec := make([]byte, 3+inner)
	sec[0] = 0x02
	sec[1] = 0xB0 | byte(inner>>8)
	sec[2] = byte(inner)
	sec[3], sec[4] = byte(Program>>8), byte(Program)
	sec[5] = 0xC1 | (version&0x1F)<<1
	sec[8] = byte(0xE0 | (AudioPID >> 8)) // PCR PID = audio PID
	sec[9] = byte(AudioPID & 0xFF)
	sec[10], sec[11] = 0xF0, 0x00
	copy(sec[12:], es)
	putCRC(sec)
	pkt := header(PmtPID, true, false, true, b.nextCC(PmtPID))
	copy(pkt[4:], append([]byte{0x00}, sec...))
	return b.emit(pkt)
}

// AddPMTPacket is an explicit-name alias for mid-stream retransmits.
func (b *TSBuilder) AddPMTPacket(version uint8) []byte { return b.AddPMT(version) }

// AddPCR emits an adaptation-field-only packet carrying an explicit PCR.
func (b *TSBuilder) AddPCR(pcr27m int64, discont bool) []byte {
	base := pcr27m / 300
	ext := pcr27m % 300
	pcr := make([]byte, 6)
	pcr[0] = byte(base >> 25)
	pcr[1] = byte(base >> 17)
	pcr[2] = byte(base >> 9)
	pcr[3] = byte(base >> 1)
	pcr[4] = byte(base&0x01)<<7 | byte(ext>>8)
	pcr[5] = byte(ext)
	flags := byte(0x10)
	if discont {
		flags |= 0x80
	}
	body := append([]byte{7, flags}, pcr...)
	pkt := header(AudioPID, false, true, false, b.nextCC(AudioPID))
	copy(pkt[4:], body)
	return b.emit(pkt)
}

// AddAACPES packetizes one AAC access unit into a PES (spanning TS packets as
// needed), stamped with the given 90 kHz PTS.
func (b *TSBuilder) AddAACPES(pts90k int64, au []byte) {
	pts := encodePTSTicks(pts90k)
	prefix := []byte{0x00, 0x00, 0x01, 0xC0}
	declared := 3 + len(pts) + len(au)
	prefix = append(prefix, byte(declared>>8), byte(declared))
	prefix = append(prefix, 0x80, 0x81, byte(len(pts)))
	prefix = append(prefix, pts...)
	frame := append(prefix, au...)

	const cap = tsPacketSize - 4
	for off := 0; off < len(frame); {
		n := len(frame) - off
		pusi := off == 0
		if n > cap {
			n = cap
		}
		pkt := header(AudioPID, pusi, false, true, b.nextCC(AudioPID))
		copy(pkt[4:], frame[off:off+n])
		b.emit(pkt)
		off += n
	}
}

// InjectCCJump rewrites the continuity counter of the next audio packet.
func (b *TSBuilder) InjectCCJump() {
	cur := b.cc[AudioPID]
	b.forceCC(AudioPID, (cur+5)&0x0F)
}

// Bytes returns the concatenated TS byte stream.
func (b *TSBuilder) Bytes() []byte {
	var out bytes.Buffer
	for _, p := range b.packets {
		out.Write(p)
	}
	return out.Bytes()
}

// Packets returns the per-packet slices (for targeted mutation tests).
func (b *TSBuilder) Packets() [][]byte { return b.packets }

// encodePTSTicks writes a PTS-only 5-byte 33-bit timestamp field.
func encodePTSTicks(ts90k int64) []byte {
	b := make([]byte, 5)
	b[0] = 0x21 | byte((ts90k>>29)&0x0E)
	b[1] = byte(ts90k >> 22)
	b[2] = byte((ts90k>>14)&0xFE) | 0x01
	b[3] = byte(ts90k >> 7)
	b[4] = byte((ts90k<<1)&0xFE) | 0x01
	return b
}
