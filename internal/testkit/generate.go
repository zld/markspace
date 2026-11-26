// Package testkit generates deterministic, self-contained fixtures:
// unfragmented AVC MP4 files with known keyframes and optional timeline
// gaps, a truncated container, and JSONL event feeds carrying real RTP
// packets. No external sample files are referenced.
package testkit

import (
	"encoding/binary"
)

const (
	// FixtureTimescale matches the acceptance RTP clock so anchor math
	// stays integer-exact.
	FixtureTimescale = 90000
	frameDelta       = 3000 // 30 fps
	frameCount       = 360  // 12 seconds
	keyframeEvery    = 30   // one sync sample per second
	width            = 320
	height           = 240
)

// boxBuilder accumulates a complete MP4 in memory in bounded pieces. The
// fixtures are small (a few KB), so whole-file assembly keeps the writer
// logic trivial while sample tables stay fully controlled.
type boxBuilder struct {
	buf []byte
}

func (b *boxBuilder) bytes(typ string, payload []byte) {
	var hdr [8]byte
	binary.BigEndian.PutUint32(hdr[0:4], uint32(8+len(payload)))
	copy(hdr[4:8], typ)
	b.buf = append(b.buf, hdr[:]...)
	b.buf = append(b.buf, payload...)
}

func (b *boxBuilder) fullBox(typ string, version uint8, flags uint32, payload []byte) {
	fb := make([]byte, 4+len(payload))
	fb[0] = version
	fb[1] = byte(flags >> 16)
	fb[2] = byte(flags >> 8)
	fb[3] = byte(flags)
	copy(fb[4:], payload)
	b.bytes(typ, fb)
}

func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }
func fixed(v uint32) []byte { return u32(v << 16) }

var identity = [9]uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

func matrixPayload() []byte {
	out := make([]byte, 36)
	for i, m := range identity {
		binary.BigEndian.PutUint32(out[i*4:], m)
	}
	return out
}

// nalBuilder emits minimal length-prefixed AVC samples; SPS/PPS live only
// in avcC, each sample is an IDR or non-IDR slice NAL.
type nalBuilder struct {
	sps []byte
	pps []byte
}

func newNALBuilder() *nalBuilder {
	return &nalBuilder{
		sps: []byte{0x67, 0x42, 0x00, 0x0a, 0xf8, 0x41, 0xa2},
		pps: []byte{0x68, 0xce, 0x38, 0x80},
	}
}

func (n *nalBuilder) avcC() []byte {
	buf := []byte{1, n.sps[1], n.sps[2], n.sps[3], 0xFC | 3, 0xE0 | 1}
	buf = append(buf, u16(uint16(len(n.sps)))...)
	buf = append(buf, n.sps...)
	buf = append(buf, 1)
	buf = append(buf, u16(uint16(len(n.pps)))...)
	buf = append(buf, n.pps...)
	return buf
}

func (n *nalBuilder) sample(isIDR bool, frame uint32) []byte {
	nalType := byte(1)
	if isIDR {
		nalType = 5
	}
	payload := []byte{nalType, 0x88, 0x80, 0x40}
	payload = binary.BigEndian.AppendUint32(payload, frame)
	return append(u32(uint32(len(payload))), payload...)
}

// Generate writes all acceptance fixtures into outDir: clean.mp4,
// gap.mp4, truncated.mp4 and the three JSONL event feeds.
func Generate(outDir string) error {
	if err := generateMP4(mp4Spec{path: outDir + "/clean.mp4"}); err != nil {
		return err
	}
	// The doubled delta lands at frame 120 (t=4s); the gap event at t=4s
	// intersects the resulting anomaly interval.
	if err := generateMP4(mp4Spec{path: outDir + "/gap.mp4", gapAfter: 120}); err != nil {
		return err
	}
	if err := generateMP4(mp4Spec{path: outDir + "/truncated.mp4", truncated: true}); err != nil {
		return err
	}
	return generateEventFiles(outDir)
}
