// Package fixture generates controlled local MPEG-TS samples: PAT/PMT
// sections with valid MPEG-2 CRC, fragmented tables, continuity gaps
// and remapped PIDs. No external files are referenced.
package fixture

import (
	"sort"

	"tsaudit/internal/psi"
)

// Layout of the demo stream.
const (
	ProgramNumber   = 1
	PMTPID          = 4096
	VideoPID        = 256
	AudioPIDOld     = 257
	AudioPIDNew     = 258
	StreamTypeVideo = 0x1B // H.264 video
	StreamTypeAudio = 0x03 // MPEG-1 audio
)

// Stream is one elementary stream entry of a generated PMT.
type Stream struct {
	Type byte
	PID  uint16
}

// PATSection builds a complete, CRC-valid PAT section.
func PATSection(version int, programs map[uint16]uint16) []byte {
	secLen := 5 + 4*len(programs) + 4
	b := []byte{psi.TableIDPAT, 0xB0 | byte(secLen>>8), byte(secLen),
		0x00, 0x01, 0xC1 | byte(version<<1), 0x00, 0x00}
	progs := make([]int, 0, len(programs))
	for p := range programs {
		progs = append(progs, int(p))
	}
	sort.Ints(progs)
	for _, p := range progs {
		pid := programs[uint16(p)]
		b = append(b, byte(p>>8), byte(p), 0xE0|byte(pid>>8), byte(pid))
	}
	return appendCRC(b)
}

// PMTSection builds a complete, CRC-valid PMT section. The first
// stream's PID doubles as PCR PID.
func PMTSection(version, program int, streams []Stream) []byte {
	secLen := 9 + 5*len(streams) + 4
	pcr := streams[0].PID
	b := []byte{psi.TableIDPMT, 0xB0 | byte(secLen>>8), byte(secLen),
		byte(program >> 8), byte(program), 0xC1 | byte(version<<1), 0x00, 0x00,
		0xE0 | byte(pcr>>8), byte(pcr), 0xF0, 0x00}
	for _, s := range streams {
		b = append(b, s.Type, 0xE0|byte(s.PID>>8), byte(s.PID), 0xF0, 0x00)
	}
	return appendCRC(b)
}

// Resection renumbers a section (section_number / last_section_number)
// and recomputes its CRC, for tests that split a table across several
// section numbers.
func Resection(section []byte, secNum, lastSec int) []byte {
	out := append([]byte(nil), section...)
	out[6], out[7] = byte(secNum), byte(lastSec)
	return appendCRC(out[:len(out)-4])
}

func appendCRC(b []byte) []byte {
	c := psi.CRC(b)
	return append(b, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
}

// Builder assembles a TS stream packet by packet, tracking continuity
// counters per PID.
type Builder struct {
	cc  map[uint16]int
	buf []byte
}

// NewBuilder returns an empty stream builder.
func NewBuilder() *Builder { return &Builder{cc: map[uint16]int{}} }

// Bytes returns the assembled TS stream.
func (b *Builder) Bytes() []byte { return b.buf }

// Gap simulates one lost packet on pid: the continuity counter skips a
// value without a packet being emitted.
func (b *Builder) Gap(pid uint16) { b.cc[pid]++ }

// Packet appends one raw packet with the given payload.
func (b *Builder) Packet(pid uint16, pusi bool, payload []byte) {
	pkt := make([]byte, 188)
	pkt[0] = 0x47
	if pusi {
		pkt[1] = 0x40
	}
	pkt[1] |= byte(pid >> 8)
	pkt[2] = byte(pid)
	pkt[3] = 0x10 | byte(b.cc[pid]&0x0F)
	b.cc[pid]++
	n := copy(pkt[4:], payload)
	for i := 4 + n; i < 188; i++ {
		pkt[i] = 0xFF
	}
	b.buf = append(b.buf, pkt...)
}

// Section emits a PSI section on pid, fragmented at the given byte
// offsets (no offsets: single packet).
func (b *Builder) Section(pid uint16, section []byte, splitAt ...int) {
	cuts := append(append([]int{0}, splitAt...), len(section))
	for i := 0; i+1 < len(cuts); i++ {
		frag := section[cuts[i]:cuts[i+1]]
		if i == 0 {
			b.Packet(pid, true, append([]byte{0x00}, frag...)) // pointer_field = 0
		} else {
			b.Packet(pid, false, frag)
		}
	}
}

// Media appends n elementary-stream packets on pid.
func (b *Builder) Media(pid uint16, n int) {
	payload := make([]byte, 184)
	copy(payload, []byte{0x00, 0x00, 0x01})
	for i := 0; i < n; i++ {
		b.Packet(pid, false, payload)
	}
}

// Remap builds the base demo scenario: PAT v0 + PMT v0 map video PID
// 256 and audio PID 257; after 4 packets each, PAT v1 and PMT v1
// (complete, CRC-valid) remap audio to PID 258; then video sends 3
// packets, the old audio PID 2 and the new audio PID 5.
func Remap() []byte {
	b := NewBuilder()
	b.Section(psi.PATPID, PATSection(0, map[uint16]uint16{ProgramNumber: PMTPID}))
	b.Section(PMTPID, PMTSection(0, ProgramNumber, []Stream{{StreamTypeVideo, VideoPID}, {StreamTypeAudio, AudioPIDOld}}))
	b.Media(VideoPID, 4)
	b.Media(AudioPIDOld, 4)
	b.Section(psi.PATPID, PATSection(1, map[uint16]uint16{ProgramNumber: PMTPID}))
	b.Section(PMTPID, PMTSection(1, ProgramNumber, []Stream{{StreamTypeVideo, VideoPID}, {StreamTypeAudio, AudioPIDNew}}))
	b.Media(VideoPID, 3)
	b.Media(AudioPIDOld, 2)
	b.Media(AudioPIDNew, 5)
	return b.Bytes()
}

// RemapWithGap is the Remap scenario with the new PMT fragmented
// across two packets and a one-packet continuity gap on the video PID
// inserted between the fragments. The new mapping must only take
// effect at the packet completing the table.
func RemapWithGap() []byte {
	b := NewBuilder()
	b.Section(psi.PATPID, PATSection(0, map[uint16]uint16{ProgramNumber: PMTPID}))
	b.Section(PMTPID, PMTSection(0, ProgramNumber, []Stream{{StreamTypeVideo, VideoPID}, {StreamTypeAudio, AudioPIDOld}}))
	b.Media(VideoPID, 4)
	b.Media(AudioPIDOld, 4)
	b.Section(psi.PATPID, PATSection(1, map[uint16]uint16{ProgramNumber: PMTPID}))
	b.Section(PMTPID, PMTSection(1, ProgramNumber, []Stream{{StreamTypeVideo, VideoPID}, {StreamTypeAudio, AudioPIDNew}}), 10)
	b.Gap(VideoPID) // one video packet lost between the PMT fragments
	b.Media(VideoPID, 3)
	b.Media(AudioPIDOld, 2)
	b.Media(AudioPIDNew, 5)
	return b.Bytes()
}
