package fixture

import (
	"bytes"
	"encoding/binary"
)

// boxWriter assembles ISO BMFF boxes from raw bytes. Fixtures are written by
// hand rather than via a full muxer so the exact byte layout — and therefore
// every injected anomaly — is known precisely.
type boxWriter struct{ buf bytes.Buffer }

func (w *boxWriter) Len() int       { return w.buf.Len() }
func (w *boxWriter) Bytes() []byte  { return w.buf.Bytes() }
func (w *boxWriter) write(b []byte) { w.buf.Write(b) }

func fourCC(s string) []byte {
	if len(s) != 4 {
		panic("fourCC must be 4 bytes: " + s)
	}
	return []byte(s)
}

// box wraps a payload in an 8-byte box header.
func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], fourCC(typ))
	copy(out[8:], payload)
	return out
}

// fullBox wraps a payload with version and flags.
func fullBox(typ string, version uint8, flags uint32, payload []byte) []byte {
	inner := make([]byte, 4+len(payload))
	inner[0] = version
	inner[1] = byte(flags >> 16)
	inner[2] = byte(flags >> 8)
	inner[3] = byte(flags)
	copy(inner[4:], payload)
	return box(typ, inner)
}

// container concatenates child boxes under one header.
func container(typ string, children ...[]byte) []byte {
	var body bytes.Buffer
	for _, c := range children {
		body.Write(c)
	}
	return box(typ, body.Bytes())
}

func be16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func be32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func be64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

func concat(parts ...[]byte) []byte {
	var out bytes.Buffer
	for _, p := range parts {
		out.Write(p)
	}
	return out.Bytes()
}

func matrixIdentity() []byte {
	vals := []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
	out := make([]byte, 0, 36)
	for _, v := range vals {
		out = append(out, be32(v)...)
	}
	return out
}

// languagePack encodes a three-letter language as ISO-639-2/T.
func languagePack(lang string) uint16 {
	if len(lang) != 3 {
		lang = "und"
	}
	return uint16(lang[0]&0x1f)<<10 | uint16(lang[1]&0x1f)<<5 | uint16(lang[2]&0x1f)
}

// trackSpec is the fixture-side description of one track.
type trackSpec struct {
	id         uint32
	timescale  uint32
	handler    string
	kind       string // "vide" or "soun"
	sampleDur  uint32
	sampleSize uint32
	width      uint32
	height     uint32
}

// ftypBox builds an iso6/cmfc ftyp compatible with fragmented CMAF.
func ftypBox() []byte {
	return box("ftyp", concat(
		[]byte("iso6"), be32(0x00000200),
		[]byte("iso6"), []byte("cmfc"), []byte("dash"), []byte("msdh"),
	))
}

// buildInit assembles ftyp plus moov for the given tracks.
func buildInit(tracks []trackSpec, fragmentDur uint32, zeroVideoTimescale bool) []byte {
	var w boxWriter
	w.write(ftypBox())
	var traks [][]byte
	for _, t := range tracks {
		traks = append(traks, buildTrak(t, zeroVideoTimescale && t.kind == "vide"))
	}
	mvhd := fullBox("mvhd", 0, 0, concat(
		be32(0), be32(0), be32(1000), be32(fragmentDur),
		be32(0x00010000), be16(0x0100), be16(0), be32(0), be32(0),
		matrixIdentity(),
		be32(0), be32(0), be32(0), be32(0), be32(0), be32(0),
		be32(uint32(len(tracks)+1)),
	))
	var trexes [][]byte
	for _, t := range tracks {
		trexes = append(trexes, fullBox("trex", 0, 0, concat(
			be32(t.id), be32(1), be32(t.sampleDur), be32(t.sampleSize), be32(defaultSampleFlags(true)),
		)))
	}
	moov := container("moov", append(append([][]byte{mvhd}, traks...), container("mvex", trexes...))...)
	w.write(moov)
	return w.Bytes()
}

// defaultSampleFlags marks a sync sample that does not depend on others.
func defaultSampleFlags(sync bool) uint32 {
	flags := uint32(2) << 24 // sample_depends_on = 2 (does not depend)
	if !sync {
		flags |= 0x00010000 // sample_is_non_sync_sample
	}
	return flags
}

func buildTrak(t trackSpec, zeroTimescale bool) []byte {
	timescale := t.timescale
	if zeroTimescale {
		timescale = 0
	}
	tkhd := fullBox("tkhd", 0, 0x000003, concat(
		be32(0), be32(0), be32(t.id), be32(0), be32(0),
		be32(0), be32(0), be16(0), be16(0), be16(0), be16(0),
		matrixIdentity(), be32(t.width<<16), be32(t.height<<16),
	))
	mdhd := fullBox("mdhd", 0, 0, concat(
		be32(0), be32(0), be32(timescale), be32(0), be16(languagePack("und")), be16(0),
	))
	hdlr := fullBox("hdlr", 0, 0, concat(
		be32(0), []byte(t.handler), []byte{0, 0, 0, 0, 0, 0, 0, 0, 0}, []byte("mediaroute\x00"),
	))
	var mediaHeader []byte
	if t.kind == "vide" {
		mediaHeader = fullBox("vmhd", 0, 0x000001, concat(be16(0), be16(0), be16(0), be16(0)))
	} else {
		mediaHeader = fullBox("smhd", 0, 0, concat(be16(0), be16(0)))
	}
	dinf := container("dinf", fullBox("dref", 0, 0, concat(be32(1), fullBox("url ", 0, 0x000001, nil))))
	stsd := fullBox("stsd", 0, 0, concat(be32(1), buildSampleEntry(t)))
	stbl := container("stbl", stsd,
		fullBox("stts", 0, 0, be32(0)),
		fullBox("stsc", 0, 0, be32(0)),
		fullBox("stsz", 0, 0, concat(be32(0), be32(0))),
		fullBox("stco", 0, 0, be32(0)),
	)
	minf := container("minf", mediaHeader, dinf, stbl)
	return container("trak", tkhd, container("mdia", mdhd, hdlr, minf))
}

// buildSampleEntry emits a minimal avc1 or mp4a entry; samples are opaque
// bytes and are never decoded.
func buildSampleEntry(t trackSpec) []byte {
	if t.kind == "vide" {
		payload := concat(
			make([]byte, 6), be16(1),
			be16(0), be16(0), make([]byte, 12),
			be16(uint16(t.width)), be16(uint16(t.height)),
			be32(0x00480000), be32(0x00480000), be32(0), be16(1),
			make([]byte, 32), be16(0x0018), be16(0xFFFF),
			box("avcC", []byte{0x01, 0x42, 0x00, 0x1e, 0xff, 0xe1, 0x00, 0x04, 0x67, 0x42, 0x00, 0x1e, 0x01}),
		)
		return box("avc1", payload)
	}
	payload := concat(
		make([]byte, 6), be16(1),
		be32(0), be32(0), be16(2), be16(16), be16(0), be16(0),
		be32(t.timescale),
		box("esds", []byte{0x03, 0x19, 0x00, 0x01, 0x00, 0x04, 0x11, 0x40, 0x15,
			0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x00, 0x11, 0x90, 0x05, 0x02, 0x11, 0x90}),
	)
	return box("mp4a", payload)
}

// sampleSpec is one generated sample.
type sampleSpec struct {
	size uint32
	dur  uint32
	sync bool
	seed int64
	kind byte
}

// trafSpec describes one track's contribution to a moof.
type trafSpec struct {
	track   trackSpec
	tfdt    uint64
	samples []sampleSpec
}

// buildSegment serialises one moof+mdat pair. Sample data follows the moof in
// track order. trun data offsets are relative to the moof start, matching
// default-base-is-moof semantics.
func buildSegment(seq uint32, trafs []trafSpec) []byte {
	moof := buildMoof(seq, trafs, nil)
	dataOffsets := make([]uint32, len(trafs))
	cursor := uint32(len(moof) + 8) // mdat header precedes payload
	for i, tr := range trafs {
		dataOffsets[i] = cursor
		for _, s := range tr.samples {
			cursor += s.size
		}
	}
	moof = buildMoof(seq, trafs, dataOffsets)

	var payload bytes.Buffer
	for _, tr := range trafs {
		for _, s := range tr.samples {
			payload.Write(samplePayload(s.seed, s.kind, int(s.size)))
		}
	}
	var w boxWriter
	w.write(moof)
	w.write(box("mdat", payload.Bytes()))
	return w.Bytes()
}

// buildMoof writes mfhd plus one traf per track. When dataOffsets is nil the
// trun data offset is zero, which is only used to measure the moof size: the
// size does not change when the offsets are filled in.
func buildMoof(seq uint32, trafs []trafSpec, dataOffsets []uint32) []byte {
	mfhd := fullBox("mfhd", 0, 0, be32(seq))
	children := [][]byte{mfhd}
	for i, tr := range trafs {
		var off uint32
		if dataOffsets != nil {
			off = dataOffsets[i]
		}
		children = append(children, buildTraf(tr, off))
	}
	return container("moof", children...)
}

// buildTraf emits tfhd (default-base-is-moof, default sample flags),
// tfdt (version 1) and a trun with per-sample duration, size and flags.
func buildTraf(tr trafSpec, dataOffset uint32) []byte {
	const tfhdFlags = 0x000020 | 0x020000 // default_sample_flags | default-base-is-moof
	tfhd := fullBox("tfhd", 0, tfhdFlags, concat(be32(tr.track.id), be32(defaultSampleFlags(true))))
	tfdt := fullBox("tfdt", 1, 0, be64(tr.tfdt))
	const trunFlags = 0x000001 | 0x000100 | 0x000200 | 0x000400
	payload := concat(be32(uint32(len(tr.samples))), be32(dataOffset))
	for _, s := range tr.samples {
		payload = append(payload, concat(be32(s.dur), be32(s.size), be32(defaultSampleFlags(s.sync)))...)
	}
	return container("traf", tfhd, tfdt, fullBox("trun", 0, trunFlags, payload))
}
