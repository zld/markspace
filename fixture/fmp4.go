// Package fixture generates deterministic media fixtures — DASH/HLS
// manifests, fMP4 files and RTP captures — with injectable faults. Every
// injected fault is recorded in the Plan so the pipeline output can be
// verified against ground truth.
package fixture

import (
	"encoding/binary"
	"math/rand"
)

// box builds one MP4 box with a 32-bit size header.
func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[4:], typ)
	copy(b[8:], payload)
	return b
}

// fullbox builds a version-0/flags-0 full box unless version is set.
func fullbox(typ string, version byte, payload []byte) []byte {
	p := make([]byte, 4+len(payload))
	p[0] = version
	copy(p[4:], payload)
	return box(typ, p)
}

// nalUnit builds one AVCC length-prefixed NAL unit.
func nalUnit(typ byte, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	binary.BigEndian.PutUint32(b, uint32(1+len(payload)))
	b[4] = typ
	copy(b[5:], payload)
	return b
}

// moofBox builds moof(mfhd, traf(tfhd, tfdt)).
func moofBox(seq uint32, decodeTime uint64) []byte {
	mfhd := make([]byte, 4)
	binary.BigEndian.PutUint32(mfhd, seq)
	tfhd := make([]byte, 4)
	binary.BigEndian.PutUint32(tfhd, 1)
	tfdt := make([]byte, 8)
	binary.BigEndian.PutUint64(tfdt, decodeTime)
	traf := box("traf", append(fullbox("tfhd", 0, tfhd), fullbox("tfdt", 1, tfdt)...))
	return box("moof", append(fullbox("mfhd", 0, mfhd), traf...))
}

// SegmentSpec describes one fMP4 media segment to generate.
type SegmentSpec struct {
	DecodeTimeMs uint64
	IDR          bool // include an IDR access unit
	Corrupt      bool // append a NAL whose length prefix is invalid
}

// BuildFMP4 builds a fragmented MP4 with the given segments on a 1/1000
// timescale. It returns the file bytes and the absolute offset of the
// injected corrupt NAL length prefix, or -1 when no corruption was
// injected.
func BuildFMP4(rnd *rand.Rand, brand string, segs []SegmentSpec) (data []byte, corruptOffset int64) {
	corruptOffset = -1
	out := box("ftyp", []byte(brand+"\x00\x00\x00\x00"))
	out = append(out, box("moov", nil)...)
	for i, s := range segs {
		out = append(out, moofBox(uint32(i+1), s.DecodeTimeMs)...)
		var payload []byte
		if s.IDR {
			payload = append(payload, nalUnit(5, randBytes(rnd, 24))...)
		}
		payload = append(payload, nalUnit(1, randBytes(rnd, 16))...)
		if s.Corrupt {
			// NAL declaring 0x7fff bytes with only one byte present.
			corruptOffset = int64(len(out)) + 8 + int64(len(payload))
			payload = append(payload, 0x00, 0x00, 0x7f, 0xff, 0x65)
		}
		out = append(out, box("mdat", payload)...)
	}
	return out, corruptOffset
}

// BuildTruncatedMP4 builds a file whose last box declares a size far
// beyond the end of the file.
func BuildTruncatedMP4() []byte {
	out := box("ftyp", []byte("isom\x00\x00\x00\x00"))
	out = append(out, moofBox(1, 0)...)
	// mdat header declares 1MiB; only 4 payload bytes follow.
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr, 1<<20)
	copy(hdr[4:], "mdat")
	return append(out, append(hdr, 0x65, 0x88, 0x00, 0x01)...)
}

func randBytes(rnd *rand.Rand, n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rnd.Intn(256))
	}
	return b
}
