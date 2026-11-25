// Package fixtures generates deterministic replay inputs for the acceptance
// runs. Every byte is produced here; no binary fixtures are checked in.
package fixtures

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

func u8(v uint8) []byte   { return []byte{v} }
func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }
func zeros(n int) []byte  { return make([]byte, n) }

func cat(parts ...[]byte) []byte {
	var b bytes.Buffer
	for _, p := range parts {
		b.Write(p)
	}
	return b.Bytes()
}

// box wraps payload with an 8-byte header. A payload longer than 4GiB is not
// representable here and is a fixture programming error.
func box(typ string, payload ...[]byte) []byte {
	if len(typ) != 4 {
		panic(fmt.Sprintf("box type %q must be 4 bytes", typ))
	}
	body := cat(payload...)
	size := 8 + len(body)
	if size > 1<<31 {
		panic("fixture box too large")
	}
	return cat(u32(uint32(size)), []byte(typ), body)
}

func fullBox(typ string, version uint8, flags uint32, payload ...[]byte) []byte {
	head := cat(u8(version), u8(uint8(flags>>16)), u8(uint8(flags>>8)), u8(uint8(flags)))
	return box(typ, append([][]byte{head}, payload...)...)
}

var identityMatrix = cat(
	u32(0x00010000), u32(0), u32(0),
	u32(0), u32(0x00010000), u32(0),
	u32(0), u32(0), u32(0x40000000),
)

type sampleSpec struct {
	size    uint32
	dur     uint32
	payload []byte
}

type trackSpec struct {
	id        uint32
	handler   string
	timescale uint32
	codec     string
	samples   []sampleSpec
}

// buildMP4 lays out a fast-start file: ftyp, moov, then mdat. Chunk offsets
// live in moov and therefore depend on moov's own length, so the movie header
// is built twice with identical field widths.
func buildMP4(tracks []trackSpec, movieTimescale uint32) []byte {
	ftyp := box("ftyp", []byte("isom"), u32(512), []byte("isom"), []byte("iso2"), []byte("mp41"))

	sizes := make([]uint64, len(tracks))
	for i, tr := range tracks {
		var n uint64
		for _, s := range tr.samples {
			n += uint64(s.size)
		}
		sizes[i] = n
	}

	// First pass: zero offsets (same slice lengths) establish moov's length.
	scratch := make([][]uint32, len(tracks))
	for i, tr := range tracks {
		scratch[i] = make([]uint32, len(tr.samples))
	}
	moovLen := uint64(len(buildMoov(tracks, scratch, movieTimescale, sizes)))
	mdatPayload := uint64(8)
	for _, n := range sizes {
		mdatPayload += n
	}
	base := uint64(len(ftyp)) + moovLen + 8 // mdat header

	offsets := make([][]uint32, len(tracks))
	for i, tr := range tracks {
		offs := make([]uint32, len(tr.samples))
		cur := base
		for j, s := range tr.samples {
			offs[j] = uint32(cur)
			cur += uint64(s.size)
		}
		offsets[i] = offs
	}
	moov := buildMoov(tracks, offsets, movieTimescale, sizes)
	if uint64(len(moov)) != moovLen {
		panic("fixture moov length changed between passes")
	}

	var mdatBody bytes.Buffer
	for _, tr := range tracks {
		for _, s := range tr.samples {
			mdatBody.Write(s.payload)
		}
	}
	return cat(ftyp, moov, box("mdat", mdatBody.Bytes()))
}

func buildMoov(tracks []trackSpec, offsets [][]uint32, movieTimescale uint32, _ []uint64) []byte {
	var traks [][]byte
	var movieDuration uint64
	for i, tr := range tracks {
		var dur uint64
		var byteDur uint64
		for _, s := range tr.samples {
			dur += uint64(s.dur)
			byteDur += uint64(s.size)
		}
		_ = byteDur
		if d := dur * uint64(movieTimescale) / uint64(tr.timescale); d > movieDuration {
			movieDuration = d
		}
		traks = append(traks, buildTrak(tr, offsets[i], dur))
	}
	mvhd := fullBox("mvhd", 0, 0,
		u32(0), u32(0), u32(movieTimescale), u32(uint32(movieDuration)),
		u32(0x00010000), u16(0x0100), u16(0), zeros(8), identityMatrix, zeros(24), u32(uint32(len(tracks)+1)))
	return box("moov", append([][]byte{mvhd}, traks...)...)
}

func buildTrak(tr trackSpec, chunkOffsets []uint32, trackDuration uint64) []byte {
	tkhd := fullBox("tkhd", 0, 3,
		u32(0), u32(0), u32(tr.id), u32(0), u32(uint32(trackDuration*1000/uint64(tr.timescale))),
		zeros(8), u16(0), u16(0), u16(0), u16(0), identityMatrix, u32(0), u32(0))

	lang := cat(u16(0x55c4), u16(0)) // und
	mdhd := fullBox("mdhd", 0, 0, u32(0), u32(0), u32(tr.timescale), u32(uint32(trackDuration)), lang)

	hdlr := fullBox("hdlr", 0, 0, u32(0), []byte(tr.handler), zeros(12), []byte("replay\x00"))
	stsd := fullBox("stsd", 0, 0, u32(1), sampleEntry(tr.handler, tr.codec))

	var stts bytes.Buffer
	stts.Write(u32(uint32(len(tr.samples))))
	var stsz bytes.Buffer
	stsz.Write(u32(0))
	stsz.Write(u32(uint32(len(tr.samples))))
	for _, s := range tr.samples {
		stts.Write(u32(1))
		stts.Write(u32(s.dur))
		stsz.Write(u32(s.size))
	}
	stsc := cat(u32(1), u32(1), u32(1), u32(1)) // one entry: chunk 1, 1 sample, desc 1
	var stco bytes.Buffer
	stco.Write(u32(uint32(len(chunkOffsets))))
	for _, o := range chunkOffsets {
		stco.Write(u32(o))
	}

	stbl := box("stbl", stsd,
		fullBox("stts", 0, 0, stts.Bytes()),
		fullBox("stsc", 0, 0, stsc),
		fullBox("stsz", 0, 0, stsz.Bytes()),
		fullBox("stco", 0, 0, stco.Bytes()))
	minf := box("minf", fullBox("vmhd", 0, 1, zeros(8)), box("dinf"), stbl)
	mdia := box("mdia", mdhd, hdlr, minf)
	return box("trak", tkhd, mdia)
}

// sampleEntry produces a minimally valid visual or audio sample entry; only
// its 4-byte type is used by the kernel.
func sampleEntry(handler, codec string) []byte {
	if handler == "vide" {
		return box(codec, zeros(6), u16(1), u16(0), u16(0), zeros(12),
			u16(640), u16(360), u32(0x00010000), u32(0x00010000), u32(0),
			u16(1), zeros(32), u16(24), u16(0xffff))
	}
	return box(codec, zeros(6), u16(1), u16(0), u16(0), u16(2), u16(16), u16(0), u16(0), u32(48000))
}

// truncateBox locates the top-level box of the given type and cuts drop bytes
// out of its payload, leaving the declared size intact so the reader sees a
// box that overruns the file. It returns the damaged bytes and the byte offset
// of the truncation.
func truncateBox(data []byte, wantType string, drop int) ([]byte, int) {
	off := 0
	for off+8 <= len(data) {
		size := int(binary.BigEndian.Uint32(data[off:]))
		typ := string(data[off+4 : off+8])
		if size < 8 || off+size > len(data) {
			return nil, 0
		}
		if typ == wantType {
			if drop >= size-8 {
				return nil, 0
			}
			// Remove bytes from the middle of the payload so the declared
			// box size stays larger than the bytes that follow it.
			cut := off + 8 + (size-8)/2
			out := make([]byte, 0, len(data)-drop)
			out = append(out, data[:cut]...)
			out = append(out, data[cut+drop:]...)
			return out, cut
		}
		off += size
	}
	return nil, 0
}
