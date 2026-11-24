package fixture

import (
	"bytes"
)

func buildClassic(tracks []trackSpec, opts Options) []byte {
	ftyp := box("ftyp", append(append([]byte("isom"), []byte{0, 0, 0, 0}...), []byte("isomiso5dash")...))

	// moov precedes mdat and its size is independent of the stco values, so
	// build it once with zero offsets to learn the payload start, then rebuild
	// with the real offsets.
	zero := make([][]int64, len(tracks))
	for ti := range tracks {
		zero[ti] = make([]int64, len(tracks[ti].samples))
	}
	buildMoov := func(offsets [][]int64) []byte {
		var children [][]byte
		children = append(children, mvhdBox(uint32(len(tracks)+1)))
		for ti, tr := range tracks {
			children = append(children, trakBox(tr, offsets[ti]))
		}
		children = append(children, emsgFixtureBoxes(opts)...)
		children = append(children, udtaChapterBox())
		return container("moov", children...)
	}
	moovSize := len(buildMoov(zero))

	var mdat wbuf
	var sampleOffsets [][]int64
	cursor := int64(len(ftyp) + moovSize + 8) // ftyp + moov + mdat header
	for ti := range tracks {
		sampleOffsets = append(sampleOffsets, make([]int64, len(tracks[ti].samples)))
		for si, s := range tracks[ti].samples {
			sampleOffsets[ti][si] = cursor
			mdat.raw(s.data)
			cursor += int64(len(s.data))
		}
	}
	moov := buildMoov(sampleOffsets)
	if len(moov) != moovSize {
		panic("fixture: moov size changed with offsets")
	}

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(moov)
	out.Write(box("mdat", mdat.bytes()))
	return out.Bytes()
}

func mvhdBox(nextTrack uint32) []byte {
	var b wbuf
	b.full(0, 0)
	b.u32(0)
	b.u32(0)
	b.u32(1000)
	b.u32(5000)
	b.u32(0x00010000)
	b.u16(0x0100)
	b.u16(0)
	b.zeros(8)
	for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
		b.u32(v)
	}
	b.zeros(24)
	b.u32(nextTrack)
	return box("mvhd", b.bytes())
}
