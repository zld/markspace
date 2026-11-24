// Package fixture generates deterministic MP4, MXF and RTP sample material.
// Everything is synthesised in-process: no external files are referenced.
package fixture

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// MP4Colr is one colour declaration written as an nclx colr box.
type MP4Colr struct {
	Primaries, Transfer, Matrix uint16
	FullRange                   bool
}

// MP4Clap is a clean-aperture crop. Offsets are in stored pixels from centre.
type MP4Clap struct {
	Width, Height int
	HorizOff      int
	VertOff       int
}

// MP4TrackSpec describes one generated MP4 track.
type MP4TrackSpec struct {
	ID             string
	Kind           string // "video" or "audio"
	StoredWidth    int
	StoredHeight   int
	DisplayWidth   int
	DisplayHeight  int
	Timescale      uint32
	SampleCount    int
	SampleDuration uint32 // in media timescale units
	EditStartUnits int64  // media units; negative is clamped to an empty edit
	Rotation       int    // 0, 90, 180 or 270 clockwise
	Clap           *MP4Clap
	Colrs          []MP4Colr
}

// MP4Spec describes a whole generated MP4 file.
type MP4Spec struct {
	MovieTimescale uint32
	Tracks         []MP4TrackSpec
	// TruncateBytes removes this many bytes from the tail, producing a file
	// whose final mdat box overruns EOF.
	TruncateBytes int
}

func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func i32(v int32) []byte  { b := make([]byte, 4); binary.BigEndian.PutUint32(b, uint32(v)); return b }
func u64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
	copy(out[4:8], typ)
	return append(out, payload...)
}

func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	p := make([]byte, 4, 4+len(payload))
	p[0] = version
	p[1] = byte(flags >> 16)
	p[2] = byte(flags >> 8)
	p[3] = byte(flags)
	return box(typ, append(p, payload...))
}

func cat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func identityMatrix() []byte {
	vals := []int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}
	var out []byte
	for _, v := range vals {
		out = append(out, i32(v)...)
	}
	return out
}

func rotationMatrix(deg int) []byte {
	one := int32(0x10000)
	var m [9]int32
	switch ((deg % 360) + 360) % 360 {
	case 90:
		m = [9]int32{0, one, 0, -one, 0, 0, 0, 0, 0x40000000}
	case 180:
		m = [9]int32{-one, 0, 0, 0, -one, 0, 0, 0, 0x40000000}
	case 270:
		m = [9]int32{0, -one, 0, one, 0, 0, 0, 0, 0x40000000}
	default:
		m = [9]int32{one, 0, 0, 0, one, 0, 0, 0, 0x40000000}
	}
	var out []byte
	for _, v := range m {
		out = append(out, i32(v)...)
	}
	return out
}

func colrBox(c MP4Colr) []byte {
	var flags byte
	if c.FullRange {
		flags = 0x80
	}
	p := cat([]byte("nclx"), u16(c.Primaries), u16(c.Transfer), u16(c.Matrix), []byte{flags, 0})
	return box("colr", p)
}

func clapBox(c MP4Clap) []byte {
	p := cat(
		i32(int32(c.Width)<<16), i32(0x10000),
		i32(int32(c.Height)<<16), i32(0x10000),
		i32(int32(c.HorizOff)<<16), i32(0x10000),
		i32(int32(c.VertOff)<<16), i32(0x10000),
	)
	return box("clap", p)
}

func visualSampleEntry(spec MP4TrackSpec) []byte {
	children := cat()
	for _, c := range spec.Colrs {
		children = append(children, colrBox(c)...)
	}
	if spec.Clap != nil {
		children = append(children, clapBox(*spec.Clap)...)
	}
	children = append(children, box("pasp", cat(u32(1), u32(1)))...)

	w := spec.StoredWidth
	h := spec.StoredHeight
	if w == 0 {
		w, h = 1, 1
	}
	entry := make([]byte, 78)
	copy(entry[6:8], u16(1)) // data_reference_index
	binary.BigEndian.PutUint16(entry[24:26], uint16(w))
	binary.BigEndian.PutUint16(entry[26:28], uint16(h))
	binary.BigEndian.PutUint32(entry[28:32], 0x00480000)
	binary.BigEndian.PutUint32(entry[32:36], 0x00480000)
	binary.BigEndian.PutUint16(entry[40:42], 1) // frame_count
	entry[42] = 20                              // compressor name length
	copy(entry[43:], "archive-kernel")
	binary.BigEndian.PutUint16(entry[74:76], 0x0018) // depth
	binary.BigEndian.PutUint16(entry[76:78], 0xffff)
	return box("avc1", append(entry, children...))
}

func buildStbl(spec MP4TrackSpec) []byte {
	stsd := fullBox("stsd", 0, 0, cat(u32(1), visualSampleEntry(spec)))
	stts := fullBox("stts", 0, 0, cat(u32(1), u32(uint32(spec.SampleCount)), u32(spec.SampleDuration)))
	stsc := fullBox("stsc", 0, 0, cat(u32(1), u32(1), u32(uint32(spec.SampleCount)), u32(1)))
	stsz := fullBox("stsz", 0, 0, cat(u32(4), u32(uint32(spec.SampleCount))))
	stco := fullBox("stco", 0, 0, cat(u32(1), u32(0)))
	return box("stbl", cat(stsd, stts, stsc, stsz, stco))
}

func buildTrak(spec MP4TrackSpec, movieTimescale uint32, trackID uint32) []byte {
	mediaDur := uint64(spec.SampleCount) * uint64(spec.SampleDuration)
	movieDur := mediaDur * uint64(movieTimescale) / uint64(spec.Timescale)
	handler := "vide"
	name := "VideoHandler\x00"
	if spec.Kind == "audio" {
		handler = "soun"
		name = "SoundHandler\x00"
	}

	tkhd := fullBox("tkhd", 0, 0x3, cat(
		u32(0), u32(0), u32(trackID), u32(0), u32(uint32(movieDur)),
		u32(0), u32(0), u16(0), u16(0), u16(0), u16(0),
		rotationMatrix(spec.Rotation),
		u32(uint32(spec.DisplayWidth)<<16), u32(uint32(spec.DisplayHeight)<<16),
	))

	editStart := spec.EditStartUnits
	if editStart < 0 {
		editStart = 0
	}
	elst := fullBox("elst", 0, 0, cat(
		u32(1), u32(uint32(movieDur)), i32(int32(editStart)), u16(1), u16(0),
	))
	edts := box("edts", elst)

	lang := []byte{0x55, 0xc4, 0x00}
	mdhd := fullBox("mdhd", 0, 0, cat(
		u32(0), u32(0), u32(spec.Timescale), u32(uint32(mediaDur)), lang, u16(0),
	))
	hdlr := fullBox("hdlr", 0, 0, cat(u32(0), []byte(handler), u32(0), u32(0), u32(0), []byte(name)))
	vmhd := fullBox("vmhd", 0, 1, cat(u16(0), u16(0), u16(0), u16(0)))
	if spec.Kind == "audio" {
		vmhd = fullBox("smhd", 0, 0, cat(u16(0), u16(0)))
	}
	url := fullBox("url ", 0, 1, nil)
	dref := fullBox("dref", 0, 0, cat(u32(1), url))
	dinf := box("dinf", dref)
	minf := box("minf", cat(vmhd, dinf, buildStbl(spec)))
	mdia := box("mdia", cat(mdhd, hdlr, minf))
	return box("trak", cat(tkhd, edts, mdia))
}

// BuildMP4 renders the spec into a complete MP4 byte stream.
func BuildMP4(spec MP4Spec) []byte {
	if spec.MovieTimescale == 0 {
		spec.MovieTimescale = 1000
	}
	ftyp := box("ftyp", cat([]byte("isom"), u32(512), []byte("isom"), []byte("iso2"), []byte("avc1"), []byte("mp41")))
	var maxDur uint64
	for _, t := range spec.Tracks {
		d := uint64(t.SampleCount) * uint64(t.SampleDuration) * uint64(spec.MovieTimescale) / uint64(t.Timescale)
		if d > maxDur {
			maxDur = d
		}
	}
	mvhd := fullBox("mvhd", 0, 0, cat(
		u32(0), u32(0), u32(spec.MovieTimescale), u32(uint32(maxDur)),
		i32(0x10000), u16(0x100), u16(0), u32(0), u32(0),
		identityMatrix(), make([]byte, 24), u32(uint32(len(spec.Tracks)+1)),
	))
	var traks []byte
	for i, t := range spec.Tracks {
		traks = append(traks, buildTrak(t, spec.MovieTimescale, uint32(i+1))...)
	}
	moov := box("moov", cat(mvhd, traks))
	// Essence is synthetic but non-empty so truncation removes real payload.
	essence := bytes.Repeat([]byte{0xaa}, 512)
	mdat := box("mdat", essence)
	out := cat(ftyp, moov, mdat)
	if spec.TruncateBytes > 0 {
		if spec.TruncateBytes >= len(out) {
			panic(fmt.Sprintf("fixture: truncate %d exceeds file size %d", spec.TruncateBytes, len(out)))
		}
		out = out[:len(out)-spec.TruncateBytes]
	}
	return out
}
