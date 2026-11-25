package fixture

import "encoding/binary"

func boxHeader(typ string, size int64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint32(b[0:4], uint32(size))
	copy(b[4:8], typ)
	return b
}

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullHeader(version byte, flags uint32) []byte {
	return []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
}

func be16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}
func be32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func ftypBox() []byte {
	p := append([]byte("isom"), be32(0x200)...)
	p = append(p, []byte("isommp41")...)
	return box("ftyp", p)
}

func mvhdBox(durationMS uint32) []byte {
	p := make([]byte, 100)
	copy(p[0:4], fullHeader(0, 0))
	binary.BigEndian.PutUint32(p[12:16], 1000)
	binary.BigEndian.PutUint32(p[16:20], durationMS)
	binary.BigEndian.PutUint32(p[20:24], 0x00010000)
	binary.BigEndian.PutUint16(p[24:26], 0x0100)
	binary.BigEndian.PutUint32(p[36:40], 0x00010000)
	binary.BigEndian.PutUint32(p[52:56], 0x00010000)
	binary.BigEndian.PutUint32(p[68:72], 0x40000000)
	binary.BigEndian.PutUint32(p[96:100], 3)
	return box("mvhd", p)
}

func tkhdBox(id, durationMS uint32, volume uint16, width, height uint32) []byte {
	p := make([]byte, 84)
	copy(p[0:4], fullHeader(0, 7)) // enabled, in movie, in preview
	binary.BigEndian.PutUint32(p[12:16], id)
	binary.BigEndian.PutUint32(p[20:24], durationMS)
	binary.BigEndian.PutUint16(p[36:38], volume)
	binary.BigEndian.PutUint32(p[40:44], 0x00010000)
	binary.BigEndian.PutUint32(p[56:60], 0x00010000)
	binary.BigEndian.PutUint32(p[72:76], 0x40000000)
	binary.BigEndian.PutUint32(p[76:80], width<<16)
	binary.BigEndian.PutUint32(p[80:84], height<<16)
	return box("tkhd", p)
}

func mdhdBox(timescale, duration uint32) []byte {
	p := make([]byte, 24)
	copy(p[0:4], fullHeader(0, 0))
	binary.BigEndian.PutUint32(p[12:16], timescale)
	binary.BigEndian.PutUint32(p[16:20], duration)
	// language "und" packed representation
	binary.BigEndian.PutUint16(p[20:22], 0x55c4)
	return box("mdhd", p)
}

func hdlrBox(handler, name string) []byte {
	p := append(fullHeader(0, 0), make([]byte, 4)...)
	p = append(p, []byte(handler)...)
	p = append(p, make([]byte, 12)...)
	p = append(p, []byte(name)...)
	p = append(p, 0)
	return box("hdlr", p)
}

func videoSampleEntry(w, h uint32) []byte {
	p := make([]byte, 78)
	binary.BigEndian.PutUint16(p[6:8], 1)
	binary.BigEndian.PutUint16(p[24:26], uint16(w))
	binary.BigEndian.PutUint16(p[26:28], uint16(h))
	binary.BigEndian.PutUint32(p[28:32], 0x00480000)
	binary.BigEndian.PutUint32(p[32:36], 0x00480000)
	binary.BigEndian.PutUint16(p[40:42], 1)
	p[43] = 31 // compressor name length Pascal-style
	binary.BigEndian.PutUint16(p[74:76], 0x0018)
	binary.BigEndian.PutUint16(p[76:78], 0xffff)
	return box("avc1", p)
}

func audioSampleEntry() []byte {
	p := make([]byte, 28)
	binary.BigEndian.PutUint16(p[6:8], 1)
	binary.BigEndian.PutUint16(p[16:18], 2)
	binary.BigEndian.PutUint16(p[18:20], 16)
	binary.BigEndian.PutUint16(p[20:22], 0)
	binary.BigEndian.PutUint32(p[24:28], 48000<<16)
	return box("mp4a", p)
}

func stsdBox(entry []byte) []byte {
	p := append(fullHeader(0, 0), be32(1)...)
	p = append(p, entry...)
	return box("stsd", p)
}

func sttsBox(count uint32, delta uint32) []byte {
	p := append(fullHeader(0, 0), be32(1)...)
	p = append(p, be32(count)...)
	p = append(p, be32(delta)...)
	return box("stts", p)
}

func cttsBox(offset int64, n int) []byte {
	p := fullHeader(0, 0)
	if offset == 0 || n <= 1 {
		p = append(p, be32(1)...)
		p = append(p, be32(uint32(n))...)
		p = append(p, be32(0)...)
	} else {
		p = append(p, be32(2)...)
		p = append(p, be32(1)...)
		p = append(p, be32(uint32(offset))...)
		p = append(p, be32(uint32(n-1))...)
		p = append(p, be32(0)...)
	}
	return box("ctts", p)
}

func stssBox(spec Spec) []byte {
	var numbers []uint32
	for i := 0; i < spec.VideoSamples; i++ {
		if isKeyframe(i, spec.KeyframeEvery) {
			numbers = append(numbers, uint32(i+1))
		}
	}
	p := append(fullHeader(0, 0), be32(uint32(len(numbers)))...)
	for _, n := range numbers {
		p = append(p, be32(n)...)
	}
	return box("stss", p)
}

func stscBox() []byte {
	p := append(fullHeader(0, 0), be32(1)...)
	p = append(p, be32(1)...)
	p = append(p, be32(1)...)
	p = append(p, be32(1)...)
	return box("stsc", p)
}

func stszBox(sizes []uint32) []byte {
	p := append(fullHeader(0, 0), be32(0)...)
	p = append(p, be32(uint32(len(sizes)))...)
	for _, sz := range sizes {
		p = append(p, be32(sz)...)
	}
	return box("stsz", p)
}

func stcoBox(offsets []uint32) []byte {
	p := append(fullHeader(0, 0), be32(uint32(len(offsets)))...)
	for _, off := range offsets {
		p = append(p, be32(off)...)
	}
	return box("stco", p)
}

func elstOOBBox(spec Spec, durationMS uint32) []byte {
	mediaDuration := uint32(spec.VideoSamples) * spec.SampleDelta
	mediaTime := int32(mediaDuration - spec.SampleDelta)
	p := append(fullHeader(0, 0), be32(1)...)
	p = append(p, be32(durationMS)...)
	p = append(p, be32(uint32(mediaTime))...)
	p = append(p, be32(0x00010000)...)
	return box("elst", p)
}
