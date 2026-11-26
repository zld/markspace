// Package fixture generates deterministic synthetic MPEG-TS assets entirely
// in code, so the demo and tests never depend on external media. Each intact
// asset carries two programs and exactly four known keyframes inside a single
// whole-packet segment; the damage cases layer one well-defined fault on top
// of that valid base stream.
package fixture

import (
	"encoding/binary"
	"os"
	"path/filepath"

	"github.com/pion/rtp"

	"tsindex/internal/clock"
	"tsindex/internal/ingest"
)

// Case names accepted by the demo --cases flag.
const (
	CaseNormal          = "normal"
	CasePCRWrap         = "pcr-wrap"
	CaseCCGap           = "cc-gap"
	CasePSIUpdate       = "psi-update"
	CaseTruncated       = "truncated"
	CaseBadCRC          = "bad-crc"
	CaseIncompleteIndex = "incomplete-index"
)

// AllCases lists every generator in stable demo order.
var AllCases = []string{
	CaseNormal, CasePCRWrap, CaseCCGap, CasePSIUpdate,
	CaseTruncated, CaseBadCRC, CaseIncompleteIndex,
}

const (
	patPID       = 0x0000
	pmtPID1      = 0x1000
	pmtPID2      = 0x1001
	videoPID1    = 0x0100
	videoPID2    = 0x0200
	programCount = 2
	keyframeCount = 4
	// SegmentPackets is the fixed, whole-packet segment size every intact
	// asset is built to: 3 PSI packets + 4 keyframe packets + 1 anchor.
	SegmentPackets = 8
)

// Spec configures one generated asset.
type Spec struct {
	Name             string
	PCRWrap          bool
	CCGap            bool
	PSIUpdate        bool
	Truncate         int // bytes removed from the tail; 0 means intact
	BadCRC           bool
	UnsupportedCodec bool
}

type tsBuilder struct {
	buf []byte
	cc  map[uint16]uint8
}

func newBuilder() *tsBuilder { return &tsBuilder{cc: map[uint16]uint8{}} }

func (b *tsBuilder) packet(pid uint16, pusi bool, pcr *int64, payload []byte) {
	var p [ingest.PacketSize]byte
	p[0] = ingest.SyncByte
	if pusi {
		p[1] = 0x40
	}
	p[1] |= byte(pid >> 8)
	p[2] = byte(pid)
	cc := b.cc[pid]
	b.cc[pid] = (cc + 1) & 0x0F
	if pcr != nil {
		p[3] = 0x30 | cc
		p[4] = 7
		p[5] = 0x10
		base := *pcr / 300
		ext := *pcr % 300
		p[6] = byte(base >> 25)
		p[7] = byte(base >> 17)
		p[8] = byte(base >> 9)
		p[9] = byte(base >> 1)
		p[10] = byte(base&1) << 7
		p[10] |= byte(ext >> 8)
		p[11] = byte(ext)
		copy(p[12:], payload)
	} else {
		p[3] = 0x10 | cc
		copy(p[4:], payload)
	}
	b.buf = append(b.buf, p[:]...)
}

func (b *tsBuilder) forceCC(pid uint16, cc uint8) { b.cc[pid] = cc }

func pat(version uint8) []byte {
	body := []byte{0x00, 0, 0, 0x00, 0x01, byte(0xC1 | (version << 1)), 0x00, 0x00}
	body = append(body, 0x00, 0x01, 0xE0|byte(pmtPID1>>8), byte(pmtPID1&0xFF))
	body = append(body, 0x00, 0x02, 0xE0|byte(pmtPID2>>8), byte(pmtPID2&0xFF))
	sectionLen := len(body) - 3 + 4
	body[1] = 0xB0 | byte(sectionLen >> 8)
	body[2] = byte(sectionLen)
	crc := crc32MPEG2(body)
	return append(body, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func pmt(version, prog uint16, pmtPID, esPID uint16, streamType uint8) []byte {
	body := []byte{0x02, 0, 0, byte(prog >> 8), byte(prog), byte(0xC1 | (version << 1)),
		0x00, 0x00, 0xE0, byte(pmtPID), 0xF0, 0x00}
	body = append(body, streamType, 0xE0|byte(esPID>>8), byte(esPID&0xFF), 0xF0, 0x00)
	sectionLen := len(body) - 3 + 4
	body[1] = 0xB0 | byte(sectionLen >> 8)
	body[2] = byte(sectionLen)
	crc := crc32MPEG2(body)
	return append(body, byte(crc>>24), byte(crc>>16), byte(crc>>8), byte(crc))
}

func (b *tsBuilder) sectionPacket(pid uint16, section []byte) {
	payload := make([]byte, 184)
	payload[0] = 0
	copy(payload[1:], section)
	b.packet(pid, true, nil, payload)
}

// keyframePayload builds a PES unit whose first NAL is an access point:
// H.264 IDR for track 1, HEVC IDR for track 2.
func keyframePayload(codec string, marker byte) []byte {
	out := make([]byte, 184)
	pes := []byte{0, 0, 1, 0xE0, 0, 0, 0x80, 0x80, 0}
	copy(out, pes)
	var nal byte
	if codec == "hevc" {
		nal = byte(19 << 1) // HEVC IDR_W_RADL
	} else {
		nal = 0x65 // H.264 IDR slice
	}
	es := []byte{0, 0, 0, 1, nal, marker}
	copy(out[len(pes):], es)
	return out
}

// Generate writes the asset for spec into dir/<name>.ts and returns its path.
func Generate(dir string, spec Spec) (string, error) {
	b := newBuilder()
	v1Type := uint8(0x1B) // H.264
	v2Type := uint8(0x24) // HEVC
	if spec.UnsupportedCodec {
		v2Type = 0x06
	}

	// Three PSI packets establish the two programs.
	b.sectionPacket(patPID, pat(0))
	b.sectionPacket(pmtPID1, pmt(0, 1, pmtPID1, videoPID1, v1Type))
	b.sectionPacket(pmtPID2, pmt(0, 2, pmtPID2, videoPID2, v2Type))

	// Four keyframe packets, alternating tracks, each PCR stamped. The wrap
	// case drives the third PCR near the 2^33 ceiling so the fourth wraps.
	pcr := int64(27_000_000)
	stride := int64(ingest.PacketSize) * 300
	for kf := 0; kf < keyframeCount; kf++ {
		pid := uint16(videoPID1)
		codec := "h264"
		if kf%2 == 1 {
			pid = uint16(videoPID2)
			codec = "hevc"
		}
		if spec.PCRWrap && kf == 2 {
			// Sit a few ticks below the modulus so the next PCR wraps cleanly.
			pcr = clock.PCRMax - 5*300
		}
		if spec.PCRWrap && kf == 3 {
			// Small raw value after the ceiling: a genuine 2^33 wrap.
			pcr = 9 * 300
		}
		val := pcr
		b.packet(pid, true, &val, keyframePayload(codec, byte(0xA0+kf)))
		pcr += stride
	}
	// One trailing anchor packet fills the fixed 8-packet segment and gives
	// the wrap case a post-wrap PCR that keeps advancing in unified time.
	anchor := pcr
	if spec.PCRWrap {
		anchor = 12 * 300
	}
	b.packet(uint16(videoPID1), false, &anchor, make([]byte, 184))

	// Damage layers, each touching exactly one independently testable axis.
	if spec.PSIUpdate {
		// Both signaling tables get a new version, producing exactly two
		// table-level PSI updates (PAT and program 1's PMT) while only the
		// affected program's mapping epoch is invalidated.
		b.sectionPacket(patPID, pat(1))
		b.sectionPacket(pmtPID1, pmt(1, 1, pmtPID1, videoPID1, v1Type))
	}
	if spec.CCGap {
		b.forceCC(videoPID1, (b.cc[videoPID1]+3)&0x0F)
		b.packet(uint16(videoPID1), false, nil, make([]byte, 184))
	}
	if spec.BadCRC {
		bad := pat(2)
		bad[len(bad)-1] ^= 0xFF
		b.sectionPacket(patPID, bad)
	}

	out := b.buf
	if spec.Truncate > 0 {
		if spec.Truncate > len(out) {
			spec.Truncate = len(out)
		}
		out = out[:len(out)-spec.Truncate]
	}
	path := filepath.Join(dir, spec.Name+".ts")
	return path, os.WriteFile(path, out, 0o600)
}

// GenerateAll writes every requested case into dir and returns name->path.
func GenerateAll(dir string, cases []string) (map[string]string, error) {
	paths := map[string]string{}
	for _, name := range cases {
		spec := Spec{Name: name}
		switch name {
		case CaseNormal:
		case CasePCRWrap:
			spec.PCRWrap = true
		case CaseCCGap:
			spec.CCGap = true
		case CasePSIUpdate:
			spec.PSIUpdate = true
		case CaseTruncated:
			spec.Truncate = 37 // inside the required 1..187 report range
		case CaseBadCRC:
			spec.BadCRC = true
		case CaseIncompleteIndex:
			spec.UnsupportedCodec = true
		default:
			return nil, unknownCase(name)
		}
		path, err := Generate(dir, spec)
		if err != nil {
			return nil, err
		}
		paths[name] = path
	}
	return paths, nil
}

type caseError struct{ name string }

func (e caseError) Error() string { return "fixture: unknown case: " + e.name }
func unknownCase(n string) error  { return caseError{n} }

// WriteRTP wraps whole 188-byte TS packets in local length-prefixed RTP
// datagrams. Each datagram carries a fixed number of TS packets in its RTP
// payload; nothing is read from a network socket.
func WriteRTP(path string, ts []byte, packetsPerDatagram int) error {
	if packetsPerDatagram <= 0 {
		packetsPerDatagram = 7
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer out.Close()
	seq := uint16(0)
	for off := 0; off < len(ts); off += packetsPerDatagram * ingest.PacketSize {
		end := off + packetsPerDatagram*ingest.PacketSize
		if end > len(ts) {
			end = len(ts)
		}
		pkt := rtp.Packet{
			Header:  rtp.Header{Version: 2, PayloadType: 33, SequenceNumber: seq, SSRC: 0x54534958},
			Payload: ts[off:end],
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return err
		}
		var lenBuf [2]byte
		binary.BigEndian.PutUint16(lenBuf[:], uint16(len(raw)))
		if _, err := out.Write(lenBuf[:]); err != nil {
			return err
		}
		if _, err := out.Write(raw); err != nil {
			return err
		}
		seq++
	}
	return nil
}

func crc32MPEG2(b []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, x := range b {
		crc ^= uint32(x) << 24
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
