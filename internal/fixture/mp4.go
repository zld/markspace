package fixture

import (
	"bytes"
	"encoding/binary"
)

// mp4Builder accumulates nested boxes for the project MP4 subset.
type mp4Builder struct{ buf *bytes.Buffer }

func b() *mp4Builder { return &mp4Builder{buf: &bytes.Buffer{}} }

func (m *mp4Builder) bytes() []byte { return m.buf.Bytes() }

// raw writes an already-complete box (header + payload).
func (m *mp4Builder) raw(data []byte) { m.buf.Write(data) }

// box wraps a payload with a type header.
func box(boxType string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out, uint32(8+len(payload)))
	copy(out[4:8], boxType)
	copy(out[8:], payload)
	return out
}

// full prefixes a version/flags triple onto a payload body.
func full(version uint8, flags uint32, body []byte) []byte {
	out := make([]byte, 4+len(body))
	out[0] = version
	out[1] = byte(flags >> 16)
	out[2] = byte(flags >> 8)
	out[3] = byte(flags)
	copy(out[4:], body)
	return out
}

func u8(v uint8) []byte      { return []byte{v} }
func u16(v uint16) []byte    { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32(v uint32) []byte    { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func i32(v int32) []byte     { return u32(uint32(v)) }
func u64(v uint64) []byte    { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }
func rawStr(s string) []byte { return []byte(s) }

func matrixBytes(m [9]int32) []byte {
	out := make([]byte, 36)
	for i, v := range m {
		binary.BigEndian.PutUint32(out[i*4:], uint32(v))
	}
	return out
}

// BuildMP4 serializes a Spec into a project-subset MP4 byte stream.
func BuildMP4(s Spec) []byte {
	root := b()
	ftypPayload := bytes.NewBuffer(nil)
	ftypPayload.Write(rawStr("isom"))
	ftypPayload.Write(u32(0x200))
	ftypPayload.Write(rawStr("isom"))
	ftypPayload.Write(rawStr("iso2"))
	ftypPayload.Write(rawStr("mp41"))
	root.raw(box("ftyp", ftypPayload.Bytes()))
	moov := b()
	moov.raw(buildVideoTrak(s.Video))
	for _, a := range s.Audio {
		moov.raw(buildAudioTrak(a))
	}
	root.raw(box("moov", moov.bytes()))
	// mdat carries no decoded media in the subset but must parse as a box.
	root.raw(box("mdat", []byte{0, 0, 0, 0, 0, 0, 0, 0}))
	return root.bytes()
}

func buildVideoTrak(v VideoSpec) []byte {
	trak := b()
	duration := v.Frames * v.Delta
	// tkhd v0.
	tkhdBody := bytes.NewBuffer(nil)
	tkhdBody.Write(u32(0)) // creation
	tkhdBody.Write(u32(0)) // modification
	tkhdBody.Write(u32(v.TrackID))
	tkhdBody.Write(u32(0)) // reserved
	tkhdBody.Write(u32(duration))
	tkhdBody.Write(make([]byte, 8)) // reserved1
	tkhdBody.Write(i16(0))          // layer
	tkhdBody.Write(i16(0))          // alternate group
	tkhdBody.Write(i16(0))          // volume
	tkhdBody.Write(u16(0))          // reserved2
	tkhdBody.Write(matrixBytes(RotationMatrix(v.Rotation)))
	tkhdBody.Write(u32(uint32(v.Width) << 16))
	tkhdBody.Write(u32(uint32(v.Height) << 16))
	trak.raw(box("tkhd", full(0, 7, tkhdBody.Bytes())))

	mdia := b()
	mdia.raw(buildMdhd(v.Timescale, duration))
	mdia.raw(buildHdlr("vide", "VideoHandler"))
	minf := b()
	stbl := b()
	stbl.raw(buildVisualEntry(v))
	stbl.raw(buildStts(v.Frames, v.Delta))
	minf.raw(box("stbl", stbl.bytes()))
	mdia.raw(box("minf", minf.bytes()))
	trak.raw(box("mdia", mdia.bytes()))
	return box("trak", trak.bytes())
}

func buildAudioTrak(a AudioSpec) []byte {
	trak := b()
	duration := a.Samples * a.Delta
	tkhdBody := bytes.NewBuffer(nil)
	tkhdBody.Write(u32(0))
	tkhdBody.Write(u32(0))
	tkhdBody.Write(u32(a.TrackID))
	tkhdBody.Write(u32(0))
	tkhdBody.Write(u32(duration))
	tkhdBody.Write(make([]byte, 8))
	tkhdBody.Write(i16(0))
	tkhdBody.Write(i16(0))
	tkhdBody.Write(i16(0x0100)) // audio volume
	tkhdBody.Write(u16(0))
	tkhdBody.Write(matrixBytes(RotationMatrix(0)))
	tkhdBody.Write(u32(0))
	tkhdBody.Write(u32(0))
	trak.raw(box("tkhd", full(0, 7, tkhdBody.Bytes())))

	mdia := b()
	mdia.raw(buildMdhd(a.Timescale, duration))
	mdia.raw(buildHdlr("soun", "SoundHandler"))
	minf := b()
	stbl := b()
	stbl.raw(buildAudioEntry(a))
	stbl.raw(buildStts(a.Samples, a.Delta))
	minf.raw(box("stbl", stbl.bytes()))
	mdia.raw(box("minf", minf.bytes()))
	trak.raw(box("mdia", mdia.bytes()))
	return box("trak", trak.bytes())
}

func buildMdhd(timescale uint32, duration uint32) []byte {
	body := bytes.NewBuffer(nil)
	body.Write(u32(0)) // creation
	body.Write(u32(0)) // modification
	body.Write(u32(timescale))
	body.Write(u32(duration))
	body.Write(u16(0x55C4)) // language 'und'
	body.Write(u16(0))      // pre-defined
	return box("mdhd", full(0, 0, body.Bytes()))
}

func buildHdlr(handler, name string) []byte {
	body := bytes.NewBuffer(nil)
	body.Write(u32(0)) // pre_defined
	body.Write(rawStr(handler))
	body.Write(make([]byte, 12)) // reserved
	body.Write(rawStr(name))
	body.Write(u8(0)) // null terminator
	return box("hdlr", full(0, 0, body.Bytes()))
}

func buildStts(count, delta uint32) []byte {
	body := append(u32(1), u32(count)...)
	body = append(body, u32(delta)...)
	return box("stts", full(0, 0, body))
}

func buildVisualEntry(v VideoSpec) []byte {
	entry := bytes.NewBuffer(nil)
	entry.Write(make([]byte, 6))  // reserved
	entry.Write(u16(1))           // data reference index
	entry.Write(u16(0))           // pre_defined
	entry.Write(u16(0))           // reserved
	entry.Write(make([]byte, 12)) // pre_defined2
	entry.Write(u16(v.Width))
	entry.Write(u16(v.Height))
	entry.Write(u32(0x00480000))  // horiz resolution 72 dpi
	entry.Write(u32(0x00480000))  // vert resolution
	entry.Write(u32(0))           // reserved
	entry.Write(u16(1))           // frame count
	entry.Write(make([]byte, 32)) // compressor name
	entry.Write(u16(0x0018))      // depth
	entry.Write(i16(-1))          // pre_defined
	for _, c := range v.Colors {
		entry.Write(buildColr(c))
	}
	if v.Crop != nil {
		entry.Write(buildClap(v.Crop))
	}
	stsdBody := append(u32(1), box("avc1", entry.Bytes())...)
	stsd := box("stsd", full(0, 0, stsdBody))
	return stsd
}

func buildAudioEntry(a AudioSpec) []byte {
	entry := bytes.NewBuffer(nil)
	entry.Write(make([]byte, 6))
	entry.Write(u16(1))
	entry.Write(u16(0))          // entry version
	entry.Write(make([]byte, 6)) // reserved
	entry.Write(u16(a.Channels))
	entry.Write(u16(a.SampleSize))
	entry.Write(u16(0)) // pre_defined
	entry.Write(u16(0)) // reserved2
	entry.Write(u32(a.SampleRate << 16))
	stsdBody := append(u32(1), box("mp4a", entry.Bytes())...)
	return box("stsd", full(0, 0, stsdBody))
}

func buildColr(c ColorSpec) []byte {
	body := bytes.NewBuffer(nil)
	body.Write(rawStr(c.Type))
	if c.Type == "nclx" {
		body.Write(u16(c.Primaries))
		body.Write(u16(c.Transfer))
		body.Write(u16(c.Matrix))
		flag := byte(0)
		if c.FullRange {
			flag = 0x80
		}
		body.Write(u8(flag))
		body.Write(u8(0)) // reserved 7 bits
	}
	return box("colr", body.Bytes())
}

func buildClap(c *CropSpec) []byte {
	body := bytes.NewBuffer(nil)
	for _, v := range []uint32{c.CleanWidthN, c.CleanWidthD, c.CleanHeightN, c.CleanHeightD} {
		body.Write(u32(v))
	}
	body.Write(i32(c.HorizOffN))
	body.Write(i32(c.HorizOffD))
	body.Write(i32(c.VertOffN))
	body.Write(i32(c.VertOffD))
	return box("clap", body.Bytes())
}

func i16(v int16) []byte { return u16(uint16(v)) }
