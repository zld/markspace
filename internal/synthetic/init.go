package synthetic

import (
	"encoding/binary"
)

// BuildInitSegment writes a minimal ftyp+moov init segment describing one
// track. timescale must match the media fragments; kind is "video" or "audio".
func BuildInitSegment(trackID uint32, timescale uint64, kind string, w, h uint16) []byte {
	var b boxBuilder
	// ftyp
	ps := b.start("ftyp")
	b.raw([]byte("iso5"))
	writeU32(&b.buf, 0x200)
	b.raw([]byte("iso5"))
	b.raw([]byte("dash"))
	b.end(ps)

	moov := b.start("moov")
	mvhd := b.start("mvhd")
	b.raw(fullHeader(1, 0))
	writeU64(&b.buf, 0) // creation
	writeU64(&b.buf, 0) // modification
	writeU32(&b.buf, 1000)
	writeU64(&b.buf, 0) // duration unknown
	b.raw(make([]byte, 80))
	b.end(mvhd)

	trak := b.start("trak")
	tkhd := b.start("tkhd")
	b.raw(fullHeader(1, 0x07)) // enabled/in-movie/in-preview
	writeU64(&b.buf, 0)
	writeU64(&b.buf, 0)
	writeU32(&b.buf, trackID)
	writeU32(&b.buf, 0)
	writeU64(&b.buf, 0)
	b.raw(make([]byte, 16))
	// width/height as 16.16 fixed point on video tracks.
	if kind == "video" {
		writeU32(&b.buf, uint32(w)<<16)
		writeU32(&b.buf, uint32(h)<<16)
	} else {
		writeU32(&b.buf, 0)
		writeU32(&b.buf, 0)
	}
	b.end(tkhd)

	mdia := b.start("mdia")
	mdhd := b.start("mdhd")
	b.raw(fullHeader(1, 0))
	writeU64(&b.buf, 0)
	writeU64(&b.buf, 0)
	writeU32(&b.buf, uint32(timescale))
	writeU64(&b.buf, 0)
	b.raw([]byte{0x55, 0xc4}) // 'und' language padding + und
	writeU16(&b.buf, 0)
	b.end(mdhd)

	hdlr := b.start("hdlr")
	b.raw(fullHeader(0, 0))
	writeU32(&b.buf, 0)
	if kind == "video" {
		b.raw([]byte("vide"))
	} else {
		b.raw([]byte("soun"))
	}
	b.raw(make([]byte, 12))
	b.raw([]byte(kind + "-track"))
	b.raw([]byte{0})
	b.end(hdlr)

	minf := b.start("minf")
	stbl := b.start("stbl")
	// stsd with a bare sample entry carrying handler type as fourcc.
	stsd := b.start("stsd")
	b.raw(fullHeader(0, 0))
	writeU32(&b.buf, 1)
	entry := b.start(sampleEntryType(kind))
	b.raw(make([]byte, 6)) // reserved
	writeU16(&b.buf, 1)    // data reference index
	b.raw(make([]byte, 16))
	b.end(entry)
	b.end(stsd)
	// Empty sample tables: fragments carry all timing via trun.
	writeEmptyTable(&b, "stts")
	writeEmptyTable(&b, "stsc")
	writeEmptyTable(&b, "stsz")
	writeEmptyTable(&b, "stco")
	b.end(stbl)
	b.end(minf)
	b.end(mdia)
	b.end(trak)

	mvex := b.start("mvex")
	trex := b.start("trex")
	b.raw(fullHeader(0, 0))
	writeU32(&b.buf, trackID)
	writeU32(&b.buf, 1) // default sample description index
	writeU32(&b.buf, 0)
	writeU32(&b.buf, 0)
	writeU32(&b.buf, 0)
	b.end(trex)
	b.end(mvex)
	b.end(moov)
	return b.buf.Bytes()
}

func writeU16(w interface{ Write([]byte) (int, error) }, v uint16) {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	w.Write(b)
}

func writeEmptyTable(b *boxBuilder, typ string) {
	s := b.start(typ)
	b.raw(fullHeader(0, 0))
	writeU32(&b.buf, 0)
	b.end(s)
}

func sampleEntryType(kind string) string {
	if kind == "video" {
		return "avc1"
	}
	return "mp4a"
}
