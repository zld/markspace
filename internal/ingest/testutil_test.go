package ingest

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// writeTruncatedBox writes an ftyp header that claims 64 bytes but stops after
// the 8-byte header, exercising the truncation path without fixtures.
func writeTruncatedBox(t *testing.T, dir string) string {
	t.Helper()
	buf := make([]byte, 8)
	binary.BigEndian.PutUint32(buf, 64)
	copy(buf[4:], "ftyp")
	path := filepath.Join(dir, "truncated.mp4")
	if err := os.WriteFile(path, buf, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeUnsupportedTrack builds a minimal container whose only track declares a
// hint handler, which the kernel must refuse with an offset.
func writeUnsupportedTrack(t *testing.T, dir string) string {
	t.Helper()
	b := newBoxBuilder()
	b.box("ftyp", []byte("isom"), u32b(512), []byte("isom"))
	b.box("mdat", bytes.Repeat([]byte{0}, 16))
	moov := newBoxBuilder()
	moov.fullBox("mvhd", 0, 0, u32b(0), u32b(0), u32b(1000), u32b(1000),
		u32b(0x00010000), u16b(0x0100), u16b(0), make([]byte, 8), matrix(), make([]byte, 24), u32b(2))
	trak := newBoxBuilder()
	trak.fullBox("tkhd", 0, 3, u32b(0), u32b(0), u32b(1), u32b(0), u32b(1000),
		make([]byte, 8), u16b(0), u16b(0), u16b(0), u16b(0), matrix(), u32b(0), u32b(0))
	mdia := newBoxBuilder()
	mdia.fullBox("mdhd", 0, 0, u32b(0), u32b(0), u32b(1000), u32b(1000), u16b(0x55c4), u16b(0))
	mdia.fullBox("hdlr", 0, 0, u32b(0), []byte("hint"), make([]byte, 12), []byte("hint\x00"))
	minf := newBoxBuilder()
	stbl := newBoxBuilder()
	stbl.fullBox("stsd", 0, 0, u32b(1), boxBytes("rtp ", make([]byte, 8)))
	stbl.fullBox("stts", 0, 0, u32b(1), u32b(1), u32b(1000))
	stbl.fullBox("stsc", 0, 0, u32b(1), u32b(1), u32b(1), u32b(1))
	stbl.fullBox("stsz", 0, 0, u32b(0), u32b(1), u32b(16))
	stbl.fullBox("stco", 0, 0, u32b(1), u32b(24))
	minf.box("stbl", stbl.bytes())
	mdia.box("minf", minf.bytes())
	trak.box("mdia", mdia.bytes())
	moov.box("trak", trak.bytes())
	b.box("moov", moov.bytes())
	path := filepath.Join(dir, "unsupported.mp4")
	if err := os.WriteFile(path, b.bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}
