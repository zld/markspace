// Package fixture synthesises MPEG-TS masters deterministically from a seed.
//
// The generator writes real PAT/PMT sections with valid CRCs, PES packet
// headers carrying PTS/DTS, PCR in the adaptation field, and periodic random
// access points. Damage is applied as explicit, located mutations, so tests
// and the demo can compare recovery against a known ground truth without
// referencing any external media file.
package fixture

import (
	"bytes"
	"fmt"
	"os"
	"sort"
)

// Stream identifiers used by every generated master.
const (
	PATPID          = 0x0000
	PMTPID          = 0x0100
	VideoPID        = 0x0101
	AudioPID        = 0x0102
	VideoStreamType = 0x1B // H.264 / AVC
	AudioStreamType = 0x0F // ADTS AAC
	PacketSize      = 188
)

// CRC32MPEG computes the CRC-32/MPEG-2 used by PSI sections.
func CRC32MPEG(b []byte) uint32 {
	var crc uint32 = 0xFFFFFFFF
	for _, c := range b {
		crc ^= uint32(c) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func withCRC(sec []byte) []byte {
	c := CRC32MPEG(sec)
	return append(sec, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
}

// section builds a long-form table section.
func section(tableID uint8, extID uint16, version uint8, body []byte) []byte {
	l := 5 + len(body) + 4
	hdr := []byte{
		tableID,
		0xB0 | byte((l>>8)&0x0F), byte(l & 0xFF),
		byte(extID >> 8), byte(extID),
		0xC0 | (version&0x1F)<<1 | 0x01,
		0x00, 0x00,
	}
	return withCRC(append(hdr, body...))
}

// PATSection returns a single-section PAT mapping program 1 to PMTPID.
func PATSection(version uint8) []byte {
	body := []byte{0x00, 0x01, 0xE0 | byte(PMTPID>>8), byte(PMTPID & 0xFF)}
	return section(0x00, 0x0001, version, body)
}

// PMTSection returns a PMT with one H.264 and one AAC stream. Video carries
// the PCR.
func PMTSection(version uint8, videoST, audioST uint8) []byte {
	body := []byte{
		0xE0 | byte(VideoPID>>8), byte(VideoPID & 0xFF), // PCR_PID
		0xF0, 0x00, // program_info_length
		videoST, 0xE0 | byte(VideoPID>>8), byte(VideoPID & 0xFF), 0xF0, 0x00,
		audioST, 0xE0 | byte(AudioPID>>8), byte(AudioPID & 0xFF), 0xF0, 0x00,
	}
	return section(0x02, 0x0001, version, body)
}

// ---- packet assembly ----

type packet struct {
	pid     uint16
	pusi    bool
	cc      uint8
	pcr     *uint64 // 27 MHz base
	rai     bool
	payload []byte // <= capacity guaranteed by the caller
}

// bytes renders exactly 188 bytes. Adaptation is present whenever a PCR,
// random-access flag or stuffing is needed.
func (p packet) bytes() []byte {
	b := make([]byte, PacketSize)
	for i := range b {
		b[i] = 0xFF
	}
	b[0] = 0x47
	b[1] = byte(p.pid>>8) & 0x1F
	if p.pusi {
		b[1] |= 0x40
	}
	b[2] = byte(p.pid)
	minAF := 0
	flags := byte(0)
	if p.pcr != nil || p.rai {
		minAF = 1
		if p.pcr != nil {
			flags |= 0x10
			minAF += 6
		}
		if p.rai {
			flags |= 0x40
		}
	}
	if minAF == 0 && len(p.payload) == PacketSize-4 {
		b[3] = 0x10 | (p.cc & 0x0F) // payload only, no adaptation
		copy(b[4:], p.payload)
		return b
	}
	// Adaptation present. Fill the packet exactly by sizing the stuffing.
	afLen := PacketSize - 5 - len(p.payload)
	if afLen < minAF {
		afLen = minAF
	}
	if afLen > 183 {
		afLen = 183
	}
	b[3] = 0x30 | (p.cc & 0x0F)
	b[4] = byte(afLen)
	b[5] = flags
	pos := 6
	if p.pcr != nil {
		v := *p.pcr & (1<<33 - 1)
		b[pos] = byte(v >> 25)
		b[pos+1] = byte(v >> 17)
		b[pos+2] = byte(v >> 9)
		b[pos+3] = byte(v >> 1)
		b[pos+4] = byte(v&1)<<7 | 0x7E
		b[pos+5] = 0x00
		pos += 6
	}
	for pos < 5+afLen {
		b[pos] = 0xFF
		pos++
	}
	copy(b[pos:], p.payload)
	return b
}

// timestampField encodes a 33-bit PTS/DTS with its 4-bit prefix.
func timestampField(prefix byte, ts int64) []byte {
	v := uint64(ts) & (1<<33 - 1)
	return []byte{
		prefix<<4 | byte((v>>30)&0x07)<<1 | 1,
		byte(v >> 22),
		byte((v>>14)&0xFF)<<1 | 1,
		byte(v >> 7),
		byte(v&0x7F)<<1 | 1,
	}
}

// videoPES builds an H.264 PES with PTS/DTS.
func videoPES(dts, pts int64, data []byte) []byte {
	hasDTS := dts != pts
	flags := byte(0x80)
	hdrLen := 5
	if hasDTS {
		flags = 0xC0
		hdrLen = 10
	}
	out := []byte{0x00, 0x00, 0x01, 0xE0}
	out = append(out, 0x00, 0x00) // placeholder for PES_packet_length
	out = append(out, flags, byte(hdrLen))
	if hasDTS {
		out = append(out, timestampField(0x3, pts)...)
		out = append(out, timestampField(0x1, dts)...)
	} else {
		out = append(out, timestampField(0x2, pts)...)
	}
	out = append(out, data...)
	pl := len(out) - 6
	out[4] = byte(pl >> 8)
	out[5] = byte(pl)
	return out
}

// audioPES builds an AAC PES carrying a PTS.
func audioPES(pts int64, data []byte) []byte {
	out := []byte{0x00, 0x00, 0x01, 0xC0, 0x00, 0x00, 0x80, 0x05}
	out = append(out, timestampField(0x2, pts)...)
	out = append(out, data...)
	pl := len(out) - 6
	out[4] = byte(pl >> 8)
	out[5] = byte(pl)
	return out
}

// ---- generation ----

// Spec describes the synthetic master.
type Spec struct {
	Seed             int64
	VideoFrames      int
	AudioFrames      int
	VideoBytes       int // elementary bytes per video frame
	AudioBytes       int // elementary bytes per audio frame
	PCRInterval      int // video frames between PCR insertions
	GOP              int // video frames between random access points
	StartPCR         uint64
	StartPTS         int64
	NoAudio          bool
	PMTChangeAt      int // frame index at which the PMT version changes; 0 disables
	PMTStartVersion  uint8
	PMTChangeVersion uint8
}

// FrameTicks used by the generator, both integral at 90 kHz.
const (
	VideoFrameTicks = 3000 // 30 fps
	AudioFrameTicks = 4286 // ~20.99 fps AAC frame cadence
	PCRToTicks      = 300
)

// DefaultSpec returns a small demo-scale master of a few hundred packets.
func DefaultSpec(seed int64) Spec {
	return Spec{
		Seed:        seed,
		VideoFrames: 60,
		AudioFrames: 120,
		VideoBytes:  1400,
		AudioBytes:  700,
		PCRInterval: 10,
		GOP:         12,
		StartPCR:    uint64(1<<33) - 90000*300*3, // three seconds before wrap
		StartPTS:    90000 * 3,
	}
}

// FrameInfo records where a generated frame landed, for verification.
type FrameInfo struct {
	Index    int
	Offset   int64
	Packet   int64
	PTS      int64
	DTS      int64
	Keyframe bool
	PID      uint16
}

// ChangePoint is a PSI version change with its located position.
type ChangePoint struct {
	Frame   int
	Offset  int64
	Version uint8
}

// Generated is a synthetic master plus its ground truth.
type Generated struct {
	Path        string
	Bytes       int
	Packets     int
	Spec        Spec
	VideoFrames []FrameInfo
	AudioFrames []FrameInfo
	PMTChanges  []ChangePoint
}

type builder struct {
	buf    bytes.Buffer
	packet int64
	cc     map[uint16]uint8
}

func newBuilder() *builder { return &builder{cc: make(map[uint16]uint8)} }

func (b *builder) write(p packet) int64 {
	off := int64(b.buf.Len())
	p.cc = b.cc[p.pid] & 0x0F
	b.cc[p.pid] = (p.cc + 1) & 0x0F
	b.buf.Write(p.bytes())
	b.packet++
	return off
}

// psi writes a section as one PUSI packet with pointer_field 0.
func (b *builder) psi(pid uint16, sec []byte) {
	_ = b.write(packet{pid: pid, pusi: true, payload: append([]byte{0x00}, sec...)})
}

// payload writes a PES payload split across packets. pcr, when non-nil, is
// placed on the first packet together with the random-access flag.
func (b *builder) payload(pid uint16, pes []byte, pcr *uint64, rai bool) int64 {
	firstOff := int64(b.buf.Len())
	first := true
	for len(pes) > 0 {
		cap := PacketSize - 4 // 184
		if first && pcr != nil {
			cap = PacketSize - 5 - 7 // adaptation flags + PCR
		} else if first && rai {
			cap = PacketSize - 5 - 1
		}
		chunk := pes
		if len(chunk) > cap {
			chunk = chunk[:cap]
		}
		var pcrArg *uint64
		raiArg := false
		if first {
			pcrArg = pcr
			raiArg = rai
		}
		b.write(packet{pid: pid, pusi: first, payload: chunk, pcr: pcrArg, rai: raiArg})
		pes = pes[len(chunk):]
		first = false
	}
	return firstOff
}

// elementary builds a deterministic per-frame payload that resembles the
// declared codec: an IDR or non-IDR NAL for video, ADTS sync for audio.
func elementary(seed int64, tag, n int, keyframe, audio bool) []byte {
	out := make([]byte, n)
	x := uint64(seed)*2654435761 + uint64(tag)*0x9E3779B97F4A7C15
	for i := range out {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		out[i] = byte(x)
	}
	if audio {
		out[0] = 0xFF
		if n > 1 {
			out[1] = 0xF1
		}
		return out
	}
	if keyframe {
		copy(out, []byte{0x00, 0x00, 0x00, 0x01, 0x65})
	} else {
		copy(out, []byte{0x00, 0x00, 0x00, 0x01, 0x41})
	}
	return out
}

// Generate writes a synthetic master to path and returns its ground truth.
func Generate(path string, spec Spec) (*Generated, error) {
	if spec.VideoFrames <= 0 {
		return nil, fmt.Errorf("fixture: VideoFrames must be positive")
	}
	if spec.GOP <= 0 {
		spec.GOP = 12
	}
	if spec.PCRInterval <= 0 {
		spec.PCRInterval = 10
	}
	if spec.VideoBytes < 32 {
		spec.VideoBytes = 1400
	}
	if spec.AudioBytes < 8 {
		spec.AudioBytes = 700
	}

	b := newBuilder()
	gen := &Generated{Spec: spec, Path: path}
	b.psi(PATPID, PATSection(0))
	b.psi(PMTPID, PMTSection(spec.PMTStartVersion, VideoStreamType, AudioStreamType))

	pcr := spec.StartPCR
	pmtVersion := spec.PMTStartVersion
	audioIdx := 0
	lastPSI := -1000

	emitPSI := func(frame, version int) {
		b.psi(PATPID, PATSection(0))
		b.psi(PMTPID, PMTSection(uint8(version), VideoStreamType, AudioStreamType))
		_ = frame
	}

	for f := 0; f < spec.VideoFrames; f++ {
		if spec.PMTChangeAt > 0 && f >= spec.PMTChangeAt && pmtVersion != spec.PMTChangeVersion {
			pmtVersion = spec.PMTChangeVersion
			gen.PMTChanges = append(gen.PMTChanges, ChangePoint{Frame: f, Offset: int64(b.buf.Len()), Version: pmtVersion})
			emitPSI(f, int(pmtVersion))
			lastPSI = f
		} else if f-lastPSI >= 6 {
			emitPSI(f, int(pmtVersion))
			lastPSI = f
		}

		keyframe := f%spec.GOP == 0
		dts := spec.StartPTS + int64(f)*VideoFrameTicks
		pts := dts + 2*VideoFrameTicks
		var pcrArg *uint64
		if f%spec.PCRInterval == 0 || keyframe {
			v := pcr
			pcrArg = &v
			pcr += uint64(VideoFrameTicks * PCRToTicks)
		}
		pes := videoPES(dts, pts, elementary(spec.Seed, f, spec.VideoBytes, keyframe, false))
		off := int64(b.buf.Len())
		b.payload(VideoPID, pes, pcrArg, keyframe)
		gen.VideoFrames = append(gen.VideoFrames, FrameInfo{
			Index: f, Offset: off, Packet: off / PacketSize,
			PTS: pts, DTS: dts, Keyframe: keyframe, PID: VideoPID,
		})

		if !spec.NoAudio {
			for a := 0; a < 2 && audioIdx < spec.AudioFrames; a++ {
				apts := spec.StartPTS + int64(audioIdx)*AudioFrameTicks
				apes := audioPES(apts, elementary(spec.Seed, 100000+audioIdx, spec.AudioBytes, false, true))
				aoff := int64(b.buf.Len())
				b.payload(AudioPID, apes, nil, false)
				gen.AudioFrames = append(gen.AudioFrames, FrameInfo{
					Index: audioIdx, Offset: aoff, Packet: aoff / PacketSize,
					PTS: apts, DTS: apts, PID: AudioPID,
				})
				audioIdx++
			}
		}
	}

	gen.Bytes = b.buf.Len()
	gen.Packets = int(b.packet)
	if err := os.WriteFile(path, b.buf.Bytes(), 0o644); err != nil {
		return nil, err
	}
	return gen, nil
}

// ---- damage ----

// Damage enumerates injectable faults and where they were placed.
type Damage struct {
	Kind   string
	Offset int64
	Packet int64
	Detail string
}

// DamageClasses lists every fault the fixture can inject.
var DamageClasses = []string{"crc", "cc", "pcrwrap", "dup", "truncpsi", "tail", "misalign"}

// Mutate copies src to dst applying the requested damage classes. src is
// opened read-only and never modified.
func Mutate(src, dst string, classes []string, seed int64) ([]Damage, error) {
	_ = seed
	b, err := os.ReadFile(src)
	if err != nil {
		return nil, err
	}
	out := append([]byte(nil), b...)
	var dmg []Damage
	for _, c := range classes {
		var ds []Damage
		out, ds, err = applyDamage(out, c)
		if err != nil {
			return nil, err
		}
		dmg = append(dmg, ds...)
	}
	if err := os.WriteFile(dst, out, 0o644); err != nil {
		return nil, err
	}
	sort.Slice(dmg, func(i, j int) bool { return dmg[i].Offset < dmg[j].Offset })
	return dmg, nil
}

func applyDamage(out []byte, class string) ([]byte, []Damage, error) {
	switch class {
	case "crc":
		// Invert the last CRC byte of the first PMT section. Framing and
		// length remain valid, so only CRC validation rejects the table.
		pkt := findPacket(out, PMTPID, 0)
		if pkt < 0 {
			return nil, nil, fmt.Errorf("fixture: no PMT packet")
		}
		start, length := sectionBounds(out, pkt)
		if start < 0 || start+length > len(out) {
			return nil, nil, fmt.Errorf("fixture: cannot bound PMT section")
		}
		out[start+length-1] ^= 0xFF
		return out, []Damage{{Kind: class, Offset: int64(start + length - 1),
			Packet: int64(start) / PacketSize, Detail: "CRC byte inverted on first PMT"}}, nil

	case "cc":
		// Skip two continuity-counter values on a mid-file video packet.
		pkt := findPacket(out, VideoPID, 6)
		if pkt < 0 {
			return nil, nil, fmt.Errorf("fixture: no video packet")
		}
		out[pkt+3] = out[pkt+3]&0xF0 | (out[pkt+3]+2)&0x0F
		return out, []Damage{{Kind: class, Offset: int64(pkt), Packet: int64(pkt) / PacketSize,
			Detail: "continuity counter advanced by two"}}, nil

	case "pcrwrap":
		// Place a PCR one second before the 33-bit boundary so unwrapping
		// must cross it while the stream continues.
		pkt := findPCR(out, 6)
		if pkt < 0 {
			return nil, nil, fmt.Errorf("fixture: no PCR to wrap")
		}
		setPCR(out, pkt, uint64(1<<33)-90000*PCRToTicks)
		return out, []Damage{{Kind: class, Offset: int64(pkt), Packet: int64(pkt) / PacketSize,
			Detail: "PCR rewritten to 1s before 33-bit wrap"}}, nil

	case "dup":
		// Insert an exact copy of a video packet. Alignment is preserved,
		// so only the continuity/duplicate logic can detect it.
		pkt := findPacket(out, VideoPID, 3)
		if pkt < 0 {
			return nil, nil, fmt.Errorf("fixture: no video packet")
		}
		cp := append([]byte(nil), out[pkt:pkt+PacketSize]...)
		out = append(out[:pkt+PacketSize], append(cp, out[pkt+PacketSize:]...)...)
		return out, []Damage{{Kind: class, Offset: int64(pkt + PacketSize),
			Packet: int64(pkt+PacketSize) / PacketSize, Detail: "verbatim duplicate inserted"}}, nil

	case "truncpsi":
		// Claim a PMT section longer than the packet payload, so reassembly
		// can never complete and the prior valid snapshot stays in force.
		pkt := findPacket(out, PMTPID, 0)
		if pkt < 0 {
			return nil, nil, fmt.Errorf("fixture: no PMT")
		}
		start, _ := sectionBounds(out, pkt)
		if start < 0 {
			return nil, nil, fmt.Errorf("fixture: cannot bound PMT section")
		}
		out[start+1] = 0xBF
		out[start+2] = 0xFF
		return out, []Damage{{Kind: class, Offset: int64(start), Packet: int64(start) / PacketSize,
			Detail: "PMT section_length claims beyond payload"}}, nil

	case "tail":
		// Drop the second half of the final packet.
		if len(out) < PacketSize {
			return nil, nil, fmt.Errorf("fixture: file too short to truncate")
		}
		n := len(out) - PacketSize/2
		off := int64(len(out))
		out = out[:n]
		return out, []Damage{{Kind: class, Offset: off, Packet: int64(off) / PacketSize,
			Detail: "final packet truncated to 94 bytes"}}, nil

	case "misalign":
		// Insert a stray byte, breaking 188-byte alignment so the scanner
		// must resynchronise.
		pkt := findPacket(out, AudioPID, 4)
		if pkt < 0 {
			pkt = findPacket(out, VideoPID, 4)
		}
		if pkt < 0 {
			return nil, nil, fmt.Errorf("fixture: no packet to misalign")
		}
		out = append(out[:pkt], append([]byte{0x00}, out[pkt:]...)...)
		return out, []Damage{{Kind: class, Offset: int64(pkt), Packet: int64(pkt) / PacketSize,
			Detail: "stray byte inserted before a packet"}}, nil
	default:
		return nil, nil, fmt.Errorf("fixture: unknown damage class %q", class)
	}
}

func findPacket(b []byte, pid uint16, n int) int {
	seen := 0
	for off := 0; off+PacketSize <= len(b); off += PacketSize {
		if b[off] != 0x47 {
			continue
		}
		if uint16(b[off+1]&0x1F)<<8|uint16(b[off+2]) != pid {
			continue
		}
		if seen == n {
			return off
		}
		seen++
	}
	return -1
}

func sectionBounds(b []byte, pktOff int) (start, length int) {
	if pktOff+5 > len(b) {
		return -1, 0
	}
	ptr := int(b[pktOff+4])
	start = pktOff + 5 + ptr
	if start+3 > len(b) {
		return -1, 0
	}
	length = 3 + int(uint16(b[start+1]&0x0F)<<8|uint16(b[start+2]))
	return start, length
}

func findPCR(b []byte, n int) int {
	seen := 0
	for off := 0; off+PacketSize <= len(b); off += PacketSize {
		if b[off] != 0x47 {
			continue
		}
		if (b[off+3]>>4)&2 == 0 {
			continue
		}
		if b[off+4] < 7 || b[off+5]&0x10 == 0 {
			continue
		}
		if seen == n {
			return off
		}
		seen++
	}
	return -1
}

func setPCR(b []byte, off int, base uint64) {
	v := base & (1<<33 - 1)
	b[off+6] = byte(v >> 25)
	b[off+7] = byte(v >> 17)
	b[off+8] = byte(v >> 9)
	b[off+9] = byte(v >> 1)
	b[off+10] = byte(v&1)<<7 | b[off+10]&0x7E
}
