// Package fixture generates all sample inputs used by the demo command and
// end-to-end tests. Data is synthesized in code: no external media files or
// network access are required, and output is byte-stable across runs.
package fixture

import (
	"bytes"
	"encoding/binary"
)

// MP4SampleCount is the per-track sample count of every generated file.
const MP4SampleCount = 20

// box writes a minimal ISO box with an explicit size and 4-char type.
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
	binary.BigEndian.PutUint32(body[16:], 0x00010000)
	body[22] = 0x01
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

func sttsBox(sampleCount, delta uint32) []byte {
	body := append(u32(1), u32(sampleCount)...)
	body = append(body, u32(delta)...)
	return fullBox("stts", 0, body)
}

func stscBox(sampleCount uint32) []byte {
	body := append(u32(1), u32(1)...)
	body = append(body, u32(sampleCount)...)
	body = append(body, u32(1)...)
	return fullBox("stsc", 0, body)
}

func stszBox(sizes []uint32) []byte {
	body := u32(0) // non-constant sizes
	body = append(body, u32(uint32(len(sizes)))...)
	for _, s := range sizes {
		body = append(body, u32(s)...)
	}
	return fullBox("stsz", 0, body)
}

func stcoBox(offset uint32) []byte {
	body := append(u32(1), u32(offset)...)
	return fullBox("stco", 0, body)
}

// TrackSpec configures one generated track.
type TrackSpec struct {
	ID        uint32
	Timescale uint32
	Duration  uint32 // mdhd duration in track ticks
	Delta     uint32 // per-sample delta (real VFR-like spacing)
	Handler   string
	Name      string
	SampleSz  uint32
}

func trakBox(spec TrackSpec, chunkOffset uint32) []byte {
	sizes := make([]uint32, MP4SampleCount)
	for i := range sizes {
		sizes[i] = spec.SampleSz
	}
	stbl := box("stbl", concat(
		sttsBox(MP4SampleCount, spec.Delta),
		stscBox(MP4SampleCount),
		stszBox(sizes),
		stcoBox(chunkOffset),
	))
	minf := box("minf", stbl)
	mdia := box("mdia", concat(
		mdhdBox(spec.Timescale, spec.Duration),
		hdlrBox(spec.Handler, spec.Name),
		minf,
	))
	return box("trak", concat(tkhdBox(spec.ID, spec.Duration), mdia))
}

func concat(parts ...[]byte) []byte {
	var out bytes.Buffer
	for _, p := range parts {
		out.Write(p)
	}
	return out.Bytes()
}

// trackTotalBytes is the mdat payload size consumed by one track.
func trackTotalBytes(sampleSz uint32) uint32 {
	return MP4SampleCount * sampleSz
}
