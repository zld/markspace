package demo

import (
	"bytes"
	"encoding/binary"
)

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[0:4], uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func fullBox(version byte, flags uint32, inner []byte) []byte {
	out := make([]byte, 4+len(inner))
	out[0] = version
	out[1] = byte(flags >> 16)
	out[2] = byte(flags >> 8)
	out[3] = byte(flags)
	copy(out[4:], inner)
	return out
}

func u32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

// FMP4Builder produces a minimal single-track AAC fMP4 byte stream.
// Each fragment carries one or more raw AAC access units.
type FMP4Builder struct {
	timescale uint32
	out       bytes.Buffer
	moofSeq   uint32
	baseTime  uint64
}

// NewFMP4Builder writes the moov header for an AAC track at timescale
// (usually the AAC sample rate).
func NewFMP4Builder(timescale uint32) *FMP4Builder {
	b := &FMP4Builder{timescale: timescale}
	mdhdPayload := make([]byte, 4+20)
	binary.BigEndian.PutUint32(mdhdPayload[12:16], timescale)
	binary.BigEndian.PutUint32(mdhdPayload[16:20], 0)
	// Minimal mp4a sample description so the track is recognizably audio.
	mp4a := box("mp4a", append(make([]byte, 28), u16(2)...))
	stsd := box("stsd", fullBox(0, 0, mp4a))
	stbl := box("stbl", stsd)
	minf := box("minf", stbl)
	hdlr := box("hdlr", fullBox(0, 0,
		append(append(make([]byte, 20), []byte("soun")...), 0)))
	mdiaChildren := append(box("mdhd", mdhdPayload), hdlr...)
	mdiaChildren = append(mdiaChildren, minf...)
	mdia := box("mdia", mdiaChildren)
	trak := box("trak", mdia)
	b.out.Write(box("moov", trak))
	return b
}

func u16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }

// AddFragment appends one moof/mdat pair. If truncateEntries is greater
// than zero, the trun declares that many extra samples whose entries are
// physically absent, simulating a truncated index.
func (b *FMP4Builder) AddFragment(units [][]byte, truncateEntries uint32) {
	b.moofSeq++
	mfhd := box("mfhd", fullBox(0, 0, u32(b.moofSeq)))
	tfdt := box("tfdt", fullBox(1, 0, u64(b.baseTime)))

	declared := uint32(len(units)) + truncateEntries
	var entries []byte
	var data []byte
	for _, u := range units {
		entries = append(entries, u32(1024)...)
		entries = append(entries, u32(uint32(len(u)))...)
		data = append(data, u...)
		b.baseTime += 1024
	}
	trunInner := append(u32(declared), u32(0)...) // count, data-offset placeholder
	trunInner = append(trunInner, entries...)
	trun := box("trun", fullBox(0, 0x301, trunInner))
	traf := box("traf", append(append(mfhd, tfdt...), trun...))
	moof := box("moof", traf)

	dataOffset := len(moof) + 8
	trunType := bytes.Index(moof, []byte("trun"))
	field := trunType + 4 + 4 + 4 // type + fullbox + samplecount
	binary.BigEndian.PutUint32(moof[field:field+4], uint32(dataOffset))

	mdat := box("mdat", data)
	b.out.Write(moof)
	b.out.Write(mdat)
}

// Bytes returns the assembled fMP4 stream.
func (b *FMP4Builder) Bytes() []byte { return b.out.Bytes() }
