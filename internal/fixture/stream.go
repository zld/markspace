// Package fixture builds deterministic MPEG-TS sample streams for the
// streamcheck demo and tests. The generator emits well-formed PAT/PMT,
// H.264 video and AAC audio with keyframes aligned to fixed segment
// boundaries, and exposes byte ranges so fault injection can target
// individual segments or packets.
package fixture

import (
	"encoding/binary"
	"time"

	"streamcheck/internal/clock"
	"streamcheck/internal/ts"
)

// Well-known PIDs used by every generated tier.
const (
	PMTPID   uint16 = 0x1000
	VideoPID uint16 = 0x0100
	AudioPID uint16 = 0x0101
)

// Tier describes one rendition the demo should produce.
type Tier struct {
	// Name is the directory-safe identifier, e.g. "900k".
	Name string
	// BandwidthBPS is the target bitrate in bits per second.
	BandwidthBPS int
}

// SegmentRange marks one segment's packets as a half-open byte interval.
type SegmentRange struct {
	Sequence int
	Start    int
	End      int
}

// BuiltStream is one generated transport stream plus its layout.
type BuiltStream struct {
	Tier      Tier
	Data      []byte
	Segments  []SegmentRange
	NumSegs   int
	SegmentMS int64
}

// crc32 builds the MPEG-2 CRC through the exported ts implementation.
func appendCRC(b []byte) []byte {
	crc := ts.CRC32MPEG2(b)
	return append(b, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func patSection(pmtPID uint16) []byte {
	b := []byte{0x00, 0, 0, 0x01, 0x00, 0xc1, 0x00, 0x00,
		0x00, 0x01, byte(pmtPID>>8) | 0xe0, byte(pmtPID)}
	binary.BigEndian.PutUint16(b[1:3], uint16(len(b)+4-3)|0x3000)
	return appendCRC(b)
}

func pmtSection(videoPID, audioPID uint16) []byte {
	b := []byte{0x02, 0, 0,
		0x00, 0x01, // program_number
		0xc1, 0x00, 0x00,
		byte(videoPID>>8) | 0xe0, byte(videoPID), // PCR_PID
		0xf0, 0x00, // program_info_length 0
		ts.StreamTypeH264,
		byte(videoPID>>8) | 0xe0, byte(videoPID),
		0xf0, 0x00, // ES_info_length 0
		ts.StreamTypeADTS,
		byte(audioPID>>8) | 0xe0, byte(audioPID),
		0xf0, 0x00,
	}
	binary.BigEndian.PutUint16(b[1:3], uint16(len(b)+4-3)|0x3000)
	return appendCRC(b)
}

// packetBuilder keeps per-PID continuity counters across a whole stream.
type packetBuilder struct {
	out []byte
	cc  map[uint16]uint8
}

func newPacketBuilder() *packetBuilder {
	return &packetBuilder{cc: make(map[uint16]uint8)}
}

// write emits one 188-byte payload-only packet; payload must be <=184 bytes.
func (b *packetBuilder) write(pid uint16, pusi bool, payload []byte) {
	if len(payload) > ts.PacketSize-4 {
		panic("fixture: payload exceeds one TS packet")
	}
	pkt := make([]byte, ts.PacketSize)
	pkt[0] = ts.SyncByte
	if pusi {
		pkt[1] = 0x40
	}
	pkt[1] |= byte(pid >> 8)
	pkt[2] = byte(pid)
	cc := b.cc[pid]
	pkt[3] = 0x10 | (cc & 0x0f)
	b.cc[pid] = cc + 1
	copy(pkt[4:4+len(payload)], payload)
	b.out = append(b.out, pkt...)
}

func encodePTS(pts uint64) []byte {
	return []byte{
		0x21 | byte((pts>>30)&0x07)<<1,
		byte(pts >> 22),
		byte((pts>>14)&0xfe) | 0x01,
		byte(pts >> 7),
		byte((pts&0x7f)<<1) | 0x01,
	}
}

// pesPacket builds a PTS-only PES unit small enough for one TS payload.
func pesPacket(streamID byte, pts uint64, payload []byte) []byte {
	opt := encodePTS(pts)
	hdr := []byte{0x00, 0x00, 0x01, streamID, 0, 0, 0x80, 0x00, byte(len(opt))}
	hdr = append(hdr, opt...)
	body := append(hdr, payload...)
	n := len(body) - 6
	body[4] = byte(n >> 8)
	body[5] = byte(n)
	return body
}

// lcgFill writes deterministic content derived from tier/segment/packet so
// every segment hashes differently while generation stays reproducible.
func lcgFill(b []byte, seed uint32) {
	x := seed
	for i := range b {
		x = x*1103515245 + 12345
		b[i] = byte(x >> 16)
	}
}

// Options controls optional stream generation features.
type Options struct {
	// PTSBase shifts every timestamp, allowing wrap scenarios to be built.
	PTSBase uint64
}

// BuildTier generates one tier's transport stream. Each of numSegs segments
// contains exactly enough 188-byte packets to match the target bitrate for
// segmentDur, with a keyframe PES at the segment's first packet.
func BuildTier(tier Tier, numSegs int, segmentDur time.Duration, opts Options) BuiltStream {
	b := newPacketBuilder()
	// PSI preamble.
	writeSection(b, ts.PIDPAT, patSection(PMTPID))
	writeSection(b, PMTPID, pmtSection(VideoPID, AudioPID))

	ticksPerSeg := uint64(segmentDur.Seconds() * float64(clock.TicksPerSecond))
	if ticksPerSeg == 0 {
		ticksPerSeg = 270_000
	}
	packetsPerSeg := packetsForBitrate(tier.BandwidthBPS, segmentDur)

	ranges := make([]SegmentRange, 0, numSegs)
	for seg := 0; seg < numSegs; seg++ {
		start := len(b.out)
		pts := opts.PTSBase + uint64(seg)*ticksPerSeg
		// Packet 0: video keyframe PES with an IDR NAL unit.
		nal := append([]byte{0x00, 0x00, 0x01, ts.H264NALIDR}, make([]byte, 16)...)
		videoPES := pesPacket(0xe0, pts, nal)
		pl := make([]byte, ts.PacketSize-4)
		copy(pl, videoPES)
		lcgFill(pl[len(videoPES):], uint32(tier.BandwidthBPS)^uint32(seg)<<8)
		b.write(VideoPID, true, pl)
		// Packet 1: AAC audio PES.
		audioPES := pesPacket(0xc0, pts, []byte{0xff, 0xf1, 0x50, 0x80})
		apl := make([]byte, ts.PacketSize-4)
		copy(apl, audioPES)
		b.write(AudioPID, true, apl)
		// Remaining packets: deterministic video continuation payload.
		for p := 2; p < packetsPerSeg; p++ {
			cpl := make([]byte, ts.PacketSize-4)
			lcgFill(cpl, uint32(tier.BandwidthBPS)^uint32(seg)<<16^uint32(p))
			b.write(VideoPID, false, cpl)
		}
		ranges = append(ranges, SegmentRange{
			Sequence: seg, Start: start, End: len(b.out),
		})
	}
	return BuiltStream{
		Tier:      tier,
		Data:      b.out,
		Segments:  ranges,
		NumSegs:   numSegs,
		SegmentMS: segmentDur.Milliseconds(),
	}
}

// packetsForBitrate derives the packet count whose 188-byte size realises
// the requested bitrate over one segment duration.
func packetsForBitrate(bitrate int, dur time.Duration) int {
	bits := float64(bitrate) * dur.Seconds()
	n := int((bits/8.0)/float64(ts.PacketSize) + 0.5)
	if n < 4 {
		n = 4
	}
	return n
}

// writeSection fragments a PSI section into PUSI-marked TS packets.
func writeSection(b *packetBuilder, pid uint16, section []byte) {
	const payloadCap = ts.PacketSize - 4
	first := true
	for len(section) > 0 {
		pl := make([]byte, payloadCap)
		off := 0
		if first {
			pl[0] = 0x00 // pointer_field
			off = 1
		}
		n := copy(pl[off:], section)
		section = section[n:]
		b.write(pid, first, pl)
		first = false
	}
}
