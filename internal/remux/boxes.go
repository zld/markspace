package remux

import (
	"bytes"
	"encoding/binary"
)

func boxBytes(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[:4], uint32(len(b)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func fullHeader(version byte, flags uint32) []byte {
	return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func ftypBox() []byte {
	p := []byte("isom")
	p = binary.BigEndian.AppendUint32(p, 0x200)
	p = append(p, "isom"...)
	p = append(p, "iso2"...)
	p = append(p, "mp41"...)
	return boxBytes("ftyp", p)
}

func mvhdBox(timescale, duration uint32) []byte {
	p := fullHeader(0, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, timescale)
	p = binary.BigEndian.AppendUint32(p, duration)
	p = binary.BigEndian.AppendUint32(p, 0x00010000)
	p = binary.BigEndian.AppendUint16(p, 0x0100)
	p = append(p, make([]byte, 10)...)
	p = append(p, identityMatrix()...)
	p = append(p, make([]byte, 24)...)
	p = binary.BigEndian.AppendUint32(p, 256)
	return boxBytes("mvhd", p)
}

func trakBox(tr Track, duration uint32, sizes, deltas, compOffsets []uint32, chunkOffset uint32) []byte {
	tkhd := tkhdBox(tr.ID, duration, tr.Width, tr.Height)
	mdhd := mdhdBox(tr.Timescale, duration)
	hdlr := hdlrBox(tr.Kind)
	stsd := stsdBox(tr)
	stts := sttsBox(deltas)
	stsc := stscBox(len(tr.Samples))
	stsz := stszBox(sizes)
	stco := stcoBox(chunkOffset)
	stbl := bytes.Join([][]byte{stsd, stts}, nil)
	if len(compOffsets) > 0 {
		stbl = append(stbl, cttsBox(compOffsets)...)
	}
	stbl = append(stbl, stsc...)
	stbl = append(stbl, stsz...)
	stbl = append(stbl, stco...)

	var minf []byte
	if tr.Kind == "video" {
		minf = append(minf, boxBytes("vmhd", append(fullHeader(0, 1), make([]byte, 8)...))...)
	} else {
		minf = append(minf, boxBytes("smhd", append(fullHeader(0, 0), make([]byte, 4)...))...)
	}
	minf = append(minf, dinfBox()...)
	minf = append(minf, boxBytes("stbl", stbl)...)
	mdia := append(boxBytes("mdhd", mdhd), boxBytes("hdlr", hdlr)...)
	mdia = append(mdia, boxBytes("minf", minf)...)
	return boxBytes("trak", append(tkhd, boxBytes("mdia", mdia)...))
}

func tkhdBox(id, duration uint32, width, height uint16) []byte {
	// Zero width/height is conventional for audio tracks.
	p := fullHeader(0, 7)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, id)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, duration)
	p = append(p, make([]byte, 8)...)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = binary.BigEndian.AppendUint16(p, 0x0100)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = append(p, identityMatrix()...)
	p = binary.BigEndian.AppendUint32(p, uint32(width)<<16)
	p = binary.BigEndian.AppendUint32(p, uint32(height)<<16)
	return boxBytes("tkhd", p)
}

func mdhdBox(ts, dur uint32) []byte {
	p := fullHeader(0, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, ts)
	p = binary.BigEndian.AppendUint32(p, dur)
	p = binary.BigEndian.AppendUint16(p, 0x55c4)
	p = binary.BigEndian.AppendUint16(p, 0)
	return p
}

func hdlrBox(kind string) []byte {
	h := "soun"
	if kind == "video" {
		h = "vide"
	}
	p := fullHeader(0, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = append(p, h...)
	p = append(p, make([]byte, 12)...)
	p = append(p, 0)
	return p
}

func dinfBox() []byte {
	url := boxBytes("url ", fullHeader(0, 1))
	p := append(fullHeader(0, 0), binary.BigEndian.AppendUint32(nil, 1)...)
	p = append(p, url...)
	return boxBytes("dinf", boxBytes("dref", p))
}

func stsdBox(tr Track) []byte {
	fourcc := tr.FourCC
	if fourcc == "" {
		if tr.Kind == "video" {
			fourcc = "avc1"
		} else {
			fourcc = "mp4a"
		}
	}
	var entry []byte
	if tr.Kind == "video" {
		entry = videoEntry(fourcc, tr.Width, tr.Height)
	} else {
		entry = audioEntry(fourcc, tr.Channels, tr.SampleRate)
	}
	p := append(fullHeader(0, 0), binary.BigEndian.AppendUint32(nil, 1)...)
	return boxBytes("stsd", append(p, entry...))
}

func videoEntry(fourcc string, w, h uint16) []byte {
	if w == 0 {
		w = 16
	}
	if h == 0 {
		h = 9
	}
	p := make([]byte, 6)
	p = binary.BigEndian.AppendUint16(p, 1)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = append(p, make([]byte, 12)...)
	p = binary.BigEndian.AppendUint16(p, w)
	p = binary.BigEndian.AppendUint16(p, h)
	p = binary.BigEndian.AppendUint32(p, 0x00480000)
	p = binary.BigEndian.AppendUint32(p, 0x00480000)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint16(p, 1)
	p = append(p, make([]byte, 32)...)
	p = binary.BigEndian.AppendUint16(p, 0x0018)
	p = binary.BigEndian.AppendUint16(p, 0xffff)
	return boxBytes(fourcc, p)
}

func audioEntry(fourcc string, channels uint16, rate uint32) []byte {
	if channels == 0 {
		channels = 2
	}
	if rate == 0 {
		rate = 48000
	}
	p := make([]byte, 6)
	p = binary.BigEndian.AppendUint16(p, 1)
	p = append(p, make([]byte, 8)...)
	p = binary.BigEndian.AppendUint16(p, channels)
	p = binary.BigEndian.AppendUint16(p, 16)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = binary.BigEndian.AppendUint32(p, rate<<16)
	return boxBytes(fourcc, p)
}

func sttsBox(deltas []uint32) []byte {
	p := fullHeader(0, 0)
	p = binary.BigEndian.AppendUint32(p, uint32(len(deltas)))
	for _, d := range deltas {
		p = binary.BigEndian.AppendUint32(p, 1)
		p = binary.BigEndian.AppendUint32(p, d)
	}
	return boxBytes("stts", p)
}
func cttsBox(offsets []uint32) []byte {
	p := fullHeader(0, 0)
	p = binary.BigEndian.AppendUint32(p, uint32(len(offsets)))
	for _, o := range offsets {
		p = binary.BigEndian.AppendUint32(p, 1)
		p = binary.BigEndian.AppendUint32(p, o)
	}
	return boxBytes("ctts", p)
}
func stscBox(n int) []byte {
	p := fullHeader(0, 0)
	p = binary.BigEndian.AppendUint32(p, 1)
	p = binary.BigEndian.AppendUint32(p, 1)
	p = binary.BigEndian.AppendUint32(p, uint32(n))
	p = binary.BigEndian.AppendUint32(p, 1)
	return boxBytes("stsc", p)
}
func stszBox(sizes []uint32) []byte {
	p := fullHeader(0, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, uint32(len(sizes)))
	for _, n := range sizes {
		p = binary.BigEndian.AppendUint32(p, n)
	}
	return boxBytes("stsz", p)
}
func stcoBox(offset uint32) []byte {
	p := fullHeader(0, 0)
	p = binary.BigEndian.AppendUint32(p, 1)
	p = binary.BigEndian.AppendUint32(p, offset)
	return boxBytes("stco", p)
}

func identityMatrix() []byte {
	m := make([]byte, 36)
	binary.BigEndian.PutUint32(m[0:], 0x00010000)
	binary.BigEndian.PutUint32(m[16:], 0x00010000)
	binary.BigEndian.PutUint32(m[32:], 0x40000000)
	return m
}

func max16(a, b uint16) uint16 {
	if a > b {
		return a
	}
	return b
}
