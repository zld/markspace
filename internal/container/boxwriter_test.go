package container

import (
	"bytes"
	"encoding/binary"
)

// Minimal ISO/IEC 14496-12 box writer used only by tests. It produces just
// enough structure for the parser: ftyp, mdat and a moov subtree with the
// sample tables mediasync validates. All integers are big-endian.

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(8+len(payload)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func fullBox(typ string, verFlags uint32, body []byte) []byte {
	payload := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(payload, verFlags)
	copy(payload[4:], body)
	return box(typ, payload)
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func ftypBox() []byte {
	payload := make([]byte, 8)
	copy(payload[0:4], "isom")
	payload = append(payload, []byte("isom")...)
	payload = append(payload, []byte("mp41")...)
	return box("ftyp", payload)
}

func mvhdBox(timescale, duration uint32) []byte {
	body := make([]byte, 96)
	binary.BigEndian.PutUint32(body[8:], timescale)
	binary.BigEndian.PutUint32(body[12:], duration)
	binary.BigEndian.PutUint32(body[16:], 0x00010000) // rate 1.0
	body[22] = 0x01                                   // volume 1.0
	return fullBox("mvhd", 0, body)
}

func tkhdBox(trackID, duration uint32) []byte {
	body := make([]byte, 80)
	binary.BigEndian.PutUint32(body[8:], trackID)
	binary.BigEndian.PutUint32(body[16:], duration)
	return fullBox("tkhd", 0, body)
}

func mdhdBox(timescale, duration uint32) []byte {
	body := make([]byte, 20)
	binary.BigEndian.PutUint32(body[8:], timescale)
	binary.BigEndian.PutUint32(body[12:], duration)
	return fullBox("mdhd", 0, body)
}

func hdlrBox(handlerType, name string) []byte {
	var body bytes.Buffer
	body.Write(u32(0))
	body.WriteString(handlerType)
	body.Write(make([]byte, 12))
	body.WriteString(name)
	body.WriteByte(0)
	return fullBox("hdlr", 0, body.Bytes())
}

func sttsBox(entries [][2]uint32) []byte {
	body := u32(uint32(len(entries)))
	for _, e := range entries {
		body = append(body, u32(e[0])...)
		body = append(body, u32(e[1])...)
	}
	return fullBox("stts", 0, body)
}

func stscBox(entries [][3]uint32) []byte {
	body := u32(uint32(len(entries)))
	for _, e := range entries {
		body = append(body, u32(e[0])...)
		body = append(body, u32(e[1])...)
		body = append(body, u32(e[2])...)
	}
	return fullBox("stsc", 0, body)
}

func stszBox(constant uint32, sizes []uint32) []byte {
	body := u32(constant)
	if constant != 0 {
		body = append(body, u32(uint32(len(sizes)))...)
	} else {
		body = append(body, u32(uint32(len(sizes)))...)
		for _, s := range sizes {
			body = append(body, u32(s)...)
		}
	}
	return fullBox("stsz", 0, body)
}

func stcoBox(offsets []uint32) []byte {
	body := u32(uint32(len(offsets)))
	for _, o := range offsets {
		body = append(body, u32(o)...)
	}
	return fullBox("stco", 0, body)
}

func trakBox(trackID, timescale, duration uint32, handler, name string,
	stts [][2]uint32, stsc [][3]uint32, sizes []uint32, offsets []uint32,
) []byte {
	stbl := box("stbl", concat(
		sttsBox(stts), stscBox(stsc), stszBox(0, sizes), stcoBox(offsets)))
	minf := box("minf", stbl)
	mdia := box("mdia", concat(mdhdBox(timescale, duration), hdlrBox(handler, name), minf))
	return box("trak", concat(tkhdBox(trackID, duration), mdia))
}

func moovBox(timescale, duration uint32, traks ...[]byte) []byte {
	payload := mvhdBox(timescale, duration)
	for _, t := range traks {
		payload = append(payload, t...)
	}
	return box("moov", payload)
}

func concat(parts ...[]byte) []byte {
	var out bytes.Buffer
	for _, p := range parts {
		out.Write(p)
	}
	return out.Bytes()
}

// buildMP4 assembles ftyp + mdat + moov with chunk offsets pointing into mdat.
// Each track owns one chunk holding all of its samples, laid out sequentially.
func buildMP4(trackSpecs []trackSpec) []byte {
	return buildMP4WithFtyp(trackSpecs, true)
}

// buildMP4NoFtyp produces an otherwise valid file whose ftyp box is missing.
func buildMP4NoFtyp(trackSpecs []trackSpec) []byte {
	return buildMP4WithFtyp(trackSpecs, false)
}

func buildMP4WithFtyp(trackSpecs []trackSpec, includeFtyp bool) []byte {
	var mdatPayload bytes.Buffer
	for _, spec := range trackSpecs {
		mdatPayload.Write(make([]byte, sumSizes(spec.sizes)))
	}

	var ftyp []byte
	if includeFtyp {
		ftyp = ftypBox()
	}
	mdat := box("mdat", mdatPayload.Bytes())
	mdatPayloadOff := uint32(len(ftyp)) + 8

	var traks [][]byte
	cursor := mdatPayloadOff
	for _, spec := range trackSpecs {
		// One chunk per track: stco carries the single chunk base offset and
		// the parser derives each sample offset from the size table.
		chunkBase := cursor
		offsets := []uint32{chunkBase}
		cursor += sumSizes(spec.sizes)
		traks = append(traks, trakBox(spec.id, spec.timescale, spec.duration,
			spec.handler, spec.name, spec.stts, spec.stsc, spec.sizes, offsets))
	}
	moov := moovBox(1000, trackSpecs[0].duration, traks...)

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(mdat)
	out.Write(moov)
	return out.Bytes()
}

func sumSizes(sizes []uint32) uint32 {
	var n uint32
	for _, s := range sizes {
		n += s
	}
	return n
}

type trackSpec struct {
	id        uint32
	timescale uint32
	duration  uint32
	handler   string
	name      string
	stts      [][2]uint32
	stsc      [][3]uint32
	sizes     []uint32
}
