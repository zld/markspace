package media

import (
	"bytes"
	"encoding/binary"
)

// enc is a tiny big-endian binary encoder used to build ISO-BMFF boxes.
// Keeping this explicit (instead of reflection-based marshalling) makes the
// generated fixtures byte-for-byte deterministic and easy to corrupt.
type enc struct {
	buf bytes.Buffer
}

func (e *enc) u8(v uint8) *enc { e.buf.WriteByte(v); return e }
func (e *enc) u16(v uint16) *enc {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	e.buf.Write(b[:])
	return e
}
func (e *enc) u32(v uint32) *enc {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	e.buf.Write(b[:])
	return e
}
func (e *enc) u64(v uint64) *enc {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	e.buf.Write(b[:])
	return e
}
func (e *enc) i16(v int16) *enc    { return e.u16(uint16(v)) }
func (e *enc) i32(v int32) *enc    { return e.u32(uint32(v)) }
func (e *enc) bytes(p []byte) *enc { e.buf.Write(p); return e }
func (e *enc) zeros(n int) *enc    { e.buf.Write(make([]byte, n)); return e }
func (e *enc) fourcc(s string) *enc {
	var b [4]byte
	copy(b[:], s)
	e.buf.Write(b[:])
	return e
}
func (e *enc) done() []byte { return e.buf.Bytes() }

// box wraps payload in a 32-bit-size box header.
func box(typ string, payload []byte) []byte {
	e := &enc{}
	e.u32(uint32(len(payload) + 8)).fourcc(typ).bytes(payload)
	return e.done()
}

// fullBox wraps payload with a one-byte version and three-byte flags.
func fullBox(typ string, ver uint8, flags uint32, payload []byte) []byte {
	f := flags & 0x00ffffff
	e := &enc{}
	e.u8(ver).u8(uint8(f >> 16)).u8(uint8(f >> 8)).u8(uint8(f)).bytes(payload)
	return box(typ, e.done())
}

// matrixIdentity is the 3x3 unity transform stored as 16.16 fixed point.
var matrixIdentity = [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

func mvhdBox(timescale uint32, duration uint64, nextID uint32, version uint8) []byte {
	e := &enc{}
	if version == 1 {
		e.u64(0).u64(0).u32(timescale).u64(duration)
	} else {
		e.u32(0).u32(0).u32(timescale).u32(uint32(duration))
	}
	e.u32(0x00010000).i16(0x0100).u16(0).u32(0).u32(0)
	for _, m := range matrixIdentity {
		e.i32(m)
	}
	e.zeros(24).u32(nextID)
	return fullBox("mvhd", version, 0, e.done())
}

func tkhdBox(id uint32, duration uint64, width, height uint16, audio bool, version uint8) []byte {
	e := &enc{}
	e.u32(0).u32(0).u32(id).u32(0)
	if version == 1 {
		e.u64(duration)
	} else {
		e.u32(uint32(duration))
	}
	e.u32(0).u32(0).i16(0).i16(0)
	if audio {
		e.i16(0x0100)
	} else {
		e.i16(0)
	}
	e.u16(0)
	for _, m := range matrixIdentity {
		e.i32(m)
	}
	e.u32(uint32(width) << 16).u32(uint32(height) << 16)
	flags := uint32(0x000007)
	return fullBox("tkhd", version, flags, e.done())
}

func mdhdBox(timescale uint32, duration uint64, lang [3]byte, version uint8) []byte {
	e := &enc{}
	if version == 1 {
		e.u64(0).u64(0).u32(timescale).u64(duration)
	} else {
		e.u32(0).u32(0).u32(timescale).u32(uint32(duration))
	}
	// packed ISO-639-2/T language: three 5-bit values offset by 0x60.
	packed := uint16(lang[0]-0x60)<<10 | uint16(lang[1]-0x60)<<5 | uint16(lang[2]-0x60)
	e.u16(packed).u16(0)
	return fullBox("mdhd", version, 0, e.done())
}

func hdlrBox(handler string, name string) []byte {
	e := &enc{}
	e.u32(0).fourcc(handler).u32(0).u32(0).u32(0).bytes([]byte(name)).u8(0)
	return fullBox("hdlr", 0, 0, e.done())
}

func elstBox(entries []EditEntry) []byte {
	e := &enc{}
	e.u32(uint32(len(entries)))
	for _, ed := range entries {
		e.u64(ed.SegmentDuration).u64(uint64(ed.MediaTime)).i16(int16(ed.Rate)).i16(0)
	}
	return fullBox("elst", 1, 0, e.done())
}

func vmhdBox() []byte {
	e := &enc{}
	e.u16(0).u16(0).u16(0).u16(0)
	return fullBox("vmhd", 0, 1, e.done())
}

func smhdBox() []byte {
	e := &enc{}
	e.u16(0).u16(0)
	return fullBox("smhd", 0, 0, e.done())
}

func dinfBox() []byte {
	dref := &enc{}
	dref.u32(1)
	dref.bytes(fullBox("url ", 0, 1, nil))
	return box("dinf", fullBox("dref", 0, 0, dref.done()))
}

func avcCBox() []byte {
	e := &enc{}
	e.u8(1).u8(0x42).u8(0x00).u8(0x1e)
	e.u8(0xff).u8(0xe1)
	sps := []byte{0x67, 0x42, 0x00, 0x1e, 0x95, 0xa8, 0x28, 0x0f}
	e.u16(uint16(len(sps))).bytes(sps)
	e.u8(1)
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	e.u16(uint16(len(pps))).bytes(pps)
	return box("avcC", e.done())
}

func visualSampleEntry(width, height uint16) []byte {
	e := &enc{}
	e.zeros(6).u16(1) // reserved + data reference index
	e.u16(0).u16(0).u32(0).u32(0).u32(0)
	e.u16(width).u16(height)
	e.u32(0x00480000).u32(0x00480000)
	e.u32(0).u16(1)
	e.zeros(32)
	e.u16(0x0018).i16(-1)
	e.bytes(avcCBox())
	return box("avc1", e.done())
}

func mp4aBox(channels uint16, sampleRate uint32) []byte {
	e := &enc{}
	e.zeros(6).u16(1)
	e.u16(0).u16(0).u16(0).u16(0)
	e.u16(channels).u16(16).u16(0).u16(0)
	e.u32(sampleRate << 16)
	// Minimal esds: version/flags plus an empty ES descriptor placeholder.
	es := &enc{}
	es.u8(0x03).u8(0x03).u16(0).u8(0)
	e.bytes(fullBox("esds", 0, 0, es.done()))
	return box("mp4a", e.done())
}

func stsdBox(codec string, width, height, channels uint16, rate uint32) []byte {
	e := &enc{}
	e.u32(1)
	if codec == "avc1" {
		e.bytes(visualSampleEntry(width, height))
	} else {
		e.bytes(mp4aBox(channels, rate))
	}
	return fullBox("stsd", 0, 0, e.done())
}

func sttsBox(durs []uint32) []byte {
	type run struct{ n, d uint32 }
	var runs []run
	for _, d := range durs {
		if n := len(runs); n > 0 && runs[n-1].d == d {
			runs[n-1].n++
			continue
		}
		runs = append(runs, run{1, d})
	}
	e := &enc{}
	e.u32(uint32(len(runs)))
	for _, r := range runs {
		e.u32(r.n).u32(r.d)
	}
	return fullBox("stts", 0, 0, e.done())
}

func cttsBox(offsets []int64) []byte {
	// offsets are signed in version 1; use version 0 only when all >= 0.
	version := uint8(0)
	for _, o := range offsets {
		if o < 0 {
			version = 1
			break
		}
	}
	type run struct {
		n uint32
		v int64
	}
	var runs []run
	for _, o := range offsets {
		if n := len(runs); n > 0 && runs[n-1].v == o {
			runs[n-1].n++
			continue
		}
		runs = append(runs, run{1, o})
	}
	e := &enc{}
	e.u32(uint32(len(runs)))
	for _, r := range runs {
		e.u32(r.n)
		if version == 1 {
			e.i32(int32(r.v))
		} else {
			e.u32(uint32(r.v))
		}
	}
	return fullBox("ctts", version, 0, e.done())
}

func stscBox(entries [][3]uint32) []byte {
	e := &enc{}
	e.u32(uint32(len(entries)))
	for _, en := range entries {
		e.u32(en[0]).u32(en[1]).u32(en[2])
	}
	return fullBox("stsc", 0, 0, e.done())
}

func stszBox(sizes []uint32) []byte {
	e := &enc{}
	e.u32(0).u32(uint32(len(sizes)))
	for _, s := range sizes {
		e.u32(s)
	}
	return fullBox("stsz", 0, 0, e.done())
}

func stcoBox(offsets []uint32) []byte {
	e := &enc{}
	e.u32(uint32(len(offsets)))
	for _, o := range offsets {
		e.u32(o)
	}
	return fullBox("stco", 0, 0, e.done())
}

func stssBox(nums []uint32) []byte {
	e := &enc{}
	e.u32(uint32(len(nums)))
	for _, n := range nums {
		e.u32(n)
	}
	return fullBox("stss", 0, 0, e.done())
}
