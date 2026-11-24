package fixture

import (
	"bytes"
	"encoding/binary"
)

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b[:4], uint32(len(b)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func fullBox(version byte, flags uint32) []byte {
	return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func buildFtyp(brand string) []byte {
	p := []byte(brand)
	p = binary.BigEndian.AppendUint32(p, 0x200)
	p = append(p, []byte("isom")...)
	p = append(p, []byte("iso2")...)
	p = append(p, []byte("mp41")...)
	return box("ftyp", p)
}

func buildMvhd(timescale, durationMS uint32) []byte {
	p := fullBox(0, 0)
	p = binary.BigEndian.AppendUint32(p, 0) // creation
	p = binary.BigEndian.AppendUint32(p, 0) // modification
	p = binary.BigEndian.AppendUint32(p, timescale)
	p = binary.BigEndian.AppendUint32(p, durationMS)
	p = binary.BigEndian.AppendUint32(p, 0x00010000) // rate 1.0
	p = binary.BigEndian.AppendUint16(p, 0x0100)     // volume 1.0
	p = append(p, make([]byte, 10)...)               // reserved
	p = append(p, identityMatrix()...)
	p = append(p, make([]byte, 24)...) // pre-defined
	p = binary.BigEndian.AppendUint32(p, 3)
	return box("mvhd", p)
}

func buildTrak(tr TrackSpec, sizes, offsets []uint32) []byte {
	duration := uint32(tr.SampleCount) * tr.SampleDuration
	tkhd := buildTkhd(tr.ID, duration)
	mdhd := buildMdhd(tr.Timescale, duration)
	hdlr := buildHdlr(tr.Kind)
	stsd := buildStsd(tr)
	stts := buildStts(tr.SampleCount, tr.SampleDuration)
	stsc := buildStsc()
	stsz := buildStsz(sizes)
	stco := buildStco(offsets)
	stbl := bytes.Join([][]byte{stsd, stts, stsc, stsz, stco}, nil)

	var minf []byte
	if tr.Kind == "video" {
		minf = append(minf, box("vmhd", append(fullBox(0, 1), make([]byte, 8)...))...)
	} else {
		smhd := append(fullBox(0, 0), make([]byte, 4)...)
		minf = append(minf, box("smhd", smhd)...)
	}
	minf = append(minf, buildDinf()...)
	minf = append(minf, box("stbl", stbl)...)

	mdia := append(box("mdhd", mdhd), box("hdlr", hdlr)...)
	mdia = append(mdia, box("minf", minf)...)
	trak := append(tkhd, box("mdia", mdia)...)
	return box("trak", trak)
}

func buildTkhd(id, duration uint32) []byte {
	p := fullBox(0, 7) // track enabled/in movie/in preview
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, id)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, duration)
	p = append(p, make([]byte, 8)...)
	p = binary.BigEndian.AppendUint16(p, 0) // layer
	p = binary.BigEndian.AppendUint16(p, 0) // alternate group
	p = binary.BigEndian.AppendUint16(p, 0x0100)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = append(p, identityMatrix()...)
	p = binary.BigEndian.AppendUint32(p, 16<<16) // width
	p = binary.BigEndian.AppendUint32(p, 9<<16)  // height
	return box("tkhd", p)
}

func buildMdhd(timescale, duration uint32) []byte {
	p := fullBox(0, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, timescale)
	p = binary.BigEndian.AppendUint32(p, duration)
	p = binary.BigEndian.AppendUint16(p, 0x55c4) // und language
	p = binary.BigEndian.AppendUint16(p, 0)
	return p
}

func buildHdlr(kind string) []byte {
	handler := "soun"
	if kind == "video" {
		handler = "vide"
	}
	p := fullBox(0, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = append(p, []byte(handler)...)
	p = append(p, make([]byte, 12)...)
	p = append(p, 0) // empty name
	return p
}

func buildDinf() []byte {
	url := box("url ", fullBox(0, 1))
	drefPayload := append(fullBox(0, 0), binary.BigEndian.AppendUint32(nil, 1)...)
	drefPayload = append(drefPayload, url...)
	dref := box("dref", drefPayload)
	return box("dinf", dref)
}

func buildStsd(tr TrackSpec) []byte {
	var entry []byte
	if tr.Kind == "video" {
		fourcc := tr.FourCC
		if fourcc == "" {
			fourcc = "avc1"
		}
		entry = buildVideoEntry(fourcc, tr.Width, tr.Height)
	} else {
		fourcc := tr.FourCC
		if fourcc == "" {
			fourcc = "mp4a"
		}
		entry = buildAudioEntry(fourcc, uint32(tr.Channels), tr.SampleRate)
	}
	p := append(fullBox(0, 0), binary.BigEndian.AppendUint32(nil, 1)...)
	p = append(p, entry...)
	return box("stsd", p)
}

func buildVideoEntry(fourcc string, w, h uint16) []byte {
	if w == 0 {
		w = 16
	}
	if h == 0 {
		h = 9
	}
	p := make([]byte, 6) // reserved
	p = binary.BigEndian.AppendUint16(p, 1)
	p = binary.BigEndian.AppendUint16(p, 0) // pre-defined
	p = binary.BigEndian.AppendUint16(p, 0) // reserved
	p = append(p, make([]byte, 12)...)
	p = binary.BigEndian.AppendUint16(p, w)
	p = binary.BigEndian.AppendUint16(p, h)
	p = binary.BigEndian.AppendUint32(p, 0x00480000)
	p = binary.BigEndian.AppendUint32(p, 0x00480000)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint16(p, 1)
	name := make([]byte, 32)
	p = append(p, name...)
	p = binary.BigEndian.AppendUint16(p, 0x0018)
	p = binary.BigEndian.AppendUint16(p, 0xffff)
	avcc := []byte{1, 0x42, 0x00, 0x1e, 0xff, 0xe1, 0x00, 0x04, 0x67, 0x42, 0x00, 0x1e, 0x01, 0x00, 0x04, 0x68, 0xce, 0x38, 0x80}
	p = append(p, box("avcC", avcc)...)
	return box(fourcc, p)
}

func buildAudioEntry(fourcc string, channels, rate uint32) []byte {
	if channels == 0 {
		channels = 2
	}
	if rate == 0 {
		rate = 48000
	}
	p := make([]byte, 6)
	p = binary.BigEndian.AppendUint16(p, 1)
	p = append(p, make([]byte, 8)...)
	p = binary.BigEndian.AppendUint16(p, uint16(channels))
	p = binary.BigEndian.AppendUint16(p, 16)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = binary.BigEndian.AppendUint16(p, 0)
	p = binary.BigEndian.AppendUint32(p, rate<<16)
	return box(fourcc, p)
}

func buildStts(count int, delta uint32) []byte {
	p := fullBox(0, 0)
	p = binary.BigEndian.AppendUint32(p, 1)
	p = binary.BigEndian.AppendUint32(p, uint32(count))
	p = binary.BigEndian.AppendUint32(p, delta)
	return box("stts", p)
}
func buildStsc() []byte {
	p := fullBox(0, 0)
	p = binary.BigEndian.AppendUint32(p, 1)
	p = binary.BigEndian.AppendUint32(p, 1)
	p = binary.BigEndian.AppendUint32(p, 1)
	p = binary.BigEndian.AppendUint32(p, 1)
	return box("stsc", p)
}
func buildStsz(sizes []uint32) []byte {
	p := fullBox(0, 0)
	p = binary.BigEndian.AppendUint32(p, 0)
	p = binary.BigEndian.AppendUint32(p, uint32(len(sizes)))
	for _, n := range sizes {
		p = binary.BigEndian.AppendUint32(p, n)
	}
	return box("stsz", p)
}
func buildStco(offsets []uint32) []byte {
	p := fullBox(0, 0)
	p = binary.BigEndian.AppendUint32(p, uint32(len(offsets)))
	for _, o := range offsets {
		p = binary.BigEndian.AppendUint32(p, o)
	}
	return box("stco", p)
}

func identityMatrix() []byte {
	m := make([]byte, 36)
	binary.BigEndian.PutUint32(m[0:], 0x00010000)
	binary.BigEndian.PutUint32(m[16:], 0x00010000)
	binary.BigEndian.PutUint32(m[32:], 0x40000000)
	return m
}
