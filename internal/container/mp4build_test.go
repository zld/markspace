package container

import (
	"bytes"
	"encoding/binary"
)

// Synthetic MP4 builder used only by tests. It produces a minimal but
// standards-shaped single-audio-track file: ftyp + moov + mdat.

type node struct {
	typ     string
	full    bool
	version byte
	flags   uint32
	payload []byte
	kids    []*node
}

func box(typ string, payload []byte, kids ...*node) *node {
	return &node{typ: typ, payload: payload, kids: kids}
}

func full(typ string, version byte, flags uint32, payload []byte, kids ...*node) *node {
	return &node{typ: typ, full: true, version: version, flags: flags, payload: payload, kids: kids}
}

func (n *node) marshal() []byte {
	var body bytes.Buffer
	if n.full {
		body.WriteByte(n.version)
		body.Write([]byte{byte(n.flags >> 16), byte(n.flags >> 8), byte(n.flags)})
	}
	body.Write(n.payload)
	for _, k := range n.kids {
		body.Write(k.marshal())
	}
	var out bytes.Buffer
	_ = binary.Write(&out, binary.BigEndian, uint32(8+body.Len()))
	out.WriteString(n.typ)
	out.Write(body.Bytes())
	return out.Bytes()
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func concat(parts ...[]byte) []byte {
	var out bytes.Buffer
	for _, p := range parts {
		out.Write(p)
	}
	return out.Bytes()
}

// buildAudioMP4 creates a file with n samples, each sampleDelta ticks long
// and sampleSize bytes of payload, at the given timescale.
func buildAudioMP4(timescale uint32, n, sampleDelta, sampleSize int) []byte {
	matrix := []int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	durMovie := uint32(n * sampleDelta * 1000 / int(timescale))
	durMedia := uint32(n * sampleDelta)

	// ftyp
	ftyp := box("ftyp", append(append([]byte("isom"), u32(0)...), []byte("isom")...))

	// mvhd
	var mvhdP bytes.Buffer
	mvhdP.Write(make([]byte, 8))
	mvhdP.Write(u32(1000))
	mvhdP.Write(u32(durMovie))
	mvhdP.Write(u32(0x00010000))
	mvhdP.Write([]byte{0x01, 0x00})
	mvhdP.Write(make([]byte, 10))
	for _, m := range matrix {
		mvhdP.Write(u32(uint32(m)))
	}
	mvhdP.Write(make([]byte, 24))
	mvhdP.Write(u32(2))
	mvhd := full("mvhd", 0, 0, mvhdP.Bytes())

	// tkhd
	var tkhdP bytes.Buffer
	tkhdP.Write(make([]byte, 8))
	tkhdP.Write(u32(1))
	tkhdP.Write(u32(0))
	tkhdP.Write(u32(durMovie))
	tkhdP.Write(make([]byte, 16))
	for _, m := range matrix {
		tkhdP.Write(u32(uint32(m)))
	}
	tkhdP.Write(make([]byte, 8))
	tkhd := full("tkhd", 0, 3, tkhdP.Bytes())

	// mdhd
	var mdhdP bytes.Buffer
	mdhdP.Write(make([]byte, 8))
	mdhdP.Write(u32(timescale))
	mdhdP.Write(u32(durMedia))
	mdhdP.Write([]byte{0x55, 0xC4, 0, 0})
	mdhd := full("mdhd", 0, 0, mdhdP.Bytes())

	// hdlr
	hdlrP := concat(u32(0), []byte("soun"), make([]byte, 12), []byte("sh\x00"))
	hdlr := full("hdlr", 0, 0, hdlrP)

	// minf children
	smhd := full("smhd", 0, 0, make([]byte, 4))
	url := full("url ", 0, 1, nil)
	dref := full("dref", 0, 0, u32(1), url)
	dinf := box("dinf", nil, dref)
	stbl := buildStbl(n, sampleDelta, sampleSize)
	minf := box("minf", nil, smhd, dinf, stbl)

	mdia := box("mdia", nil, mdhd, hdlr, minf)
	moov := box("moov", nil, mvhd, tkhd, mdia)
	mdat := box("mdat", make([]byte, n*sampleSize))

	var out bytes.Buffer
	for _, n := range []*node{ftyp, moov, mdat} {
		out.Write(n.marshal())
	}
	return out.Bytes()
}

func buildStbl(n, sampleDelta, sampleSize int) *node {
	// mp4a audio sample entry (28 payload bytes).
	var mp4aP bytes.Buffer
	mp4aP.Write(make([]byte, 6))
	mp4aP.Write(u16(1))
	mp4aP.Write(make([]byte, 8))
	mp4aP.Write(u16(2))
	mp4aP.Write(u16(16))
	mp4aP.Write(u16(0))
	mp4aP.Write(u16(0))
	mp4aP.Write(u32(48000 << 16))
	mp4a := box("mp4a", mp4aP.Bytes())
	stsd := full("stsd", 0, 0, u32(1), mp4a)

	stts := full("stts", 0, 0, concat(u32(1), u32(uint32(n)), u32(uint32(sampleDelta))))
	// one run: first chunk 1, n samples per chunk, description 1.
	stsc := full("stsc", 0, 0, concat(u32(1), u32(1), u32(uint32(n)), u32(1)))
	var stszP bytes.Buffer
	stszP.Write(u32(0))
	stszP.Write(u32(uint32(n)))
	for i := 0; i < n; i++ {
		stszP.Write(u32(uint32(sampleSize)))
	}
	stsz := full("stsz", 0, 0, stszP.Bytes())

	// Placeholder chunk offset; patched by the test to the mdat payload offset.
	stco := full("stco", 0, 0, append(u32(1), u32(0xDEADBEEF)...))
	return box("stbl", nil, stsd, stts, stsc, stsz, stco)
}
