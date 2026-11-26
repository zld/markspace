package rewrap

import (
	"bytes"
	"encoding/binary"

	"mediapipeline/pkg/media"
)

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func box(typ string, payload []byte) []byte {
	var b bytes.Buffer
	b.Write(u32(uint32(8 + len(payload))))
	b.WriteString(typ)
	b.Write(payload)
	return b.Bytes()
}

func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	return box(typ, append([]byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}, payload...))
}

func ftypBox() []byte {
	var p bytes.Buffer
	p.WriteString("isom")
	p.Write(u32(512))
	p.WriteString("isomiso2mp41")
	return box("ftyp", p.Bytes())
}

func mvhdBox(timescale uint32, duration uint64) []byte {
	var p bytes.Buffer
	if duration <= 0xffffffff {
		p.Write(u32(0))
		p.Write(u32(0))
		p.Write(u32(timescale))
		p.Write(u32(uint32(duration)))
	} else {
		// Version 1 layout is not needed by fixtures but keeps overflow explicit.
		return mvhdV1(timescale, duration)
	}
	p.Write(u32(0x00010000))
	p.Write(u16(0x0100))
	p.Write(make([]byte, 10))
	for _, m := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		p.Write(u32(m))
	}
	p.Write(make([]byte, 24))
	p.Write(u32(256))
	return fullBox("mvhd", 0, 0, p.Bytes())
}

func mvhdV1(timescale uint32, duration uint64) []byte {
	var p bytes.Buffer
	p.Write(make([]byte, 16)) // creation/modification uint64 each
	p.Write(u32(timescale))
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, duration)
	p.Write(b)
	p.Write(u32(0x00010000))
	p.Write(u16(0x0100))
	p.Write(make([]byte, 10))
	for _, m := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		p.Write(u32(m))
	}
	p.Write(make([]byte, 24))
	p.Write(u32(256))
	return fullBox("mvhd", 1, 0, p.Bytes())
}

func tkhdBox(id uint32, duration uint64) []byte {
	var p bytes.Buffer
	p.Write(u32(0))
	p.Write(u32(0))
	p.Write(u32(id))
	p.Write(u32(0))
	p.Write(u32(uint32(duration)))
	p.Write(make([]byte, 8))
	p.Write(u16(0))
	p.Write(u16(0))
	p.Write(u16(0))
	p.Write(u16(0))
	for _, m := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		p.Write(u32(m))
	}
	p.Write(u32(16 << 16))
	p.Write(u32(16 << 16))
	return fullBox("tkhd", 0, 3, p.Bytes())
}

func mdhdBox(timescale uint32, duration uint64) []byte {
	var p bytes.Buffer
	p.Write(u32(0))
	p.Write(u32(0))
	p.Write(u32(timescale))
	p.Write(u32(uint32(duration)))
	p.Write(u16(0x55c4))
	p.Write(u16(0))
	return fullBox("mdhd", 0, 0, p.Bytes())
}

func hdlrBox(handler string) []byte {
	handlerType := "vide"
	name := "VideoHandler\x00"
	if handler == "mp4a" || handler == "soun" {
		handlerType = "soun"
		name = "SoundHandler\x00"
	}
	var p bytes.Buffer
	p.Write(u32(0))
	p.WriteString(handlerType)
	p.Write(make([]byte, 12))
	p.WriteString(name)
	return fullBox("hdlr", 0, 0, p.Bytes())
}

func videoSampleEntry() []byte {
	var p bytes.Buffer
	p.Write(make([]byte, 6))
	p.Write(u16(1))
	p.Write(u16(0))
	p.Write(u16(0))
	p.Write(make([]byte, 12))
	p.Write(u16(16))
	p.Write(u16(16))
	p.Write(u32(0x00480000))
	p.Write(u32(0x00480000))
	p.Write(u32(0))
	p.Write(u16(1))
	name := make([]byte, 32)
	copy(name[1:], []byte("demo"))
	p.Write(name)
	p.Write(u16(0x0018))
	p.Write(u16(0xffff))
	return box("mp4v", p.Bytes())
}

func stsdBox() []byte {
	var p bytes.Buffer
	p.Write(u32(1))
	p.Write(videoSampleEntry())
	return fullBox("stsd", 0, 0, p.Bytes())
}

func sttsBox(samples []media.Sample) []byte {
	type run struct {
		count uint32
		delta uint32
	}
	var runs []run
	for _, s := range samples {
		if len(runs) > 0 && runs[len(runs)-1].delta == s.Duration {
			runs[len(runs)-1].count++
		} else {
			runs = append(runs, run{count: 1, delta: s.Duration})
		}
	}
	var p bytes.Buffer
	p.Write(u32(uint32(len(runs))))
	for _, r := range runs {
		p.Write(u32(r.count))
		p.Write(u32(r.delta))
	}
	return fullBox("stts", 0, 0, p.Bytes())
}

func cttsBox(samples []media.Sample) []byte {
	type run struct {
		count  uint32
		offset int32
	}
	var runs []run
	for _, s := range samples {
		o := s.PTS - s.DTS
		if o < -0x80000000 || o > 0x7fffffff {
			return nil
		}
		off := int32(o)
		if len(runs) > 0 && runs[len(runs)-1].offset == off {
			runs[len(runs)-1].count++
		} else {
			runs = append(runs, run{count: 1, offset: off})
		}
	}
	if len(runs) == 0 {
		return nil
	}
	var p bytes.Buffer
	p.Write(u32(uint32(len(runs))))
	for _, r := range runs {
		p.Write(u32(r.count))
		p.Write(u32(uint32(r.offset)))
	}
	return fullBox("ctts", 1, 0, p.Bytes())
}

func stscBox() []byte {
	var p bytes.Buffer
	p.Write(u32(1))
	p.Write(u32(1))
	p.Write(u32(1))
	p.Write(u32(1))
	return fullBox("stsc", 0, 0, p.Bytes())
}

func stszBox(samples []media.Sample) []byte {
	var p bytes.Buffer
	p.Write(u32(0))
	p.Write(u32(uint32(len(samples))))
	for _, s := range samples {
		p.Write(u32(s.Size))
	}
	return fullBox("stsz", 0, 0, p.Bytes())
}

func stssBox(samples []media.Sample) []byte {
	var nums []uint32
	for _, s := range samples {
		if s.Sync {
			nums = append(nums, s.Number)
		}
	}
	if len(nums) == 0 || len(nums) == len(samples) {
		// An absent stss box legally means every sample is sync.
		return nil
	}
	var p bytes.Buffer
	p.Write(u32(uint32(len(nums))))
	for _, n := range nums {
		p.Write(u32(n))
	}
	return fullBox("stss", 0, 0, p.Bytes())
}

func stcoBox(offsets []uint32) []byte {
	var p bytes.Buffer
	p.Write(u32(uint32(len(offsets))))
	for _, o := range offsets {
		p.Write(u32(o))
	}
	return fullBox("stco", 0, 0, p.Bytes())
}

func elstBox(edits []media.EditListEntry, timescale uint32, duration uint64) []byte {
	if len(edits) == 0 {
		var p bytes.Buffer
		p.Write(u32(1))
		p.Write(u32(uint32(duration)))
		p.Write(u32(0))
		p.Write(u32(0x00010000))
		return fullBox("elst", 0, 0, p.Bytes())
	}
	var p bytes.Buffer
	p.Write(u32(uint32(len(edits))))
	for _, e := range edits {
		p.Write(u32(uint32(e.SegmentDuration)))
		p.Write(u32(uint32(e.MediaTime)))
		p.Write(u32(0x00010000))
	}
	return fullBox("elst", 0, 0, p.Bytes())
}
