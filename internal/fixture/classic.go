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

func trakBox(tr trackSpec, offsets []int64) []byte {
	tkhd := func() []byte {
		var b wbuf
		b.full(0, 3)
		b.u32(0)
		b.u32(0)
		b.u32(tr.id)
		b.u32(0)
		var dur uint64
		for _, s := range tr.samples {
			dur += uint64(s.delta)
		}
		b.u32(uint32(dur * 1000 / uint64(tr.timescale)))
		b.zeros(8)
		b.u16(0)
		b.u16(0)
		vol := uint16(0)
		if tr.kind == "audio" {
			vol = 0x0100
		}
		b.u16(vol)
		b.u16(0)
		for _, v := range []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000} {
			b.u32(v)
		}
		b.u32(0)
		b.u32(0)
		return box("tkhd", b.bytes())
	}()

	mdhd := func() []byte {
		var b wbuf
		b.full(0, 0)
		b.u32(0)
		b.u32(0)
		b.u32(tr.timescale)
		var dur uint64
		for _, s := range tr.samples {
			dur += uint64(s.delta)
		}
		b.u32(uint32(dur))
		b.u16(0x55c4)
		b.u16(0)
		return box("mdhd", b.bytes())
	}()

	hdlr := func() []byte {
		var b wbuf
		b.full(0, 0)
		b.u32(0)
		b.str(tr.handler)
		b.zeros(12)
		b.str("Handler")
		b.u8(0)
		return box("hdlr", b.bytes())
	}()

	var minfHeader []byte
	switch tr.kind {
	case "audio":
		var b wbuf
		b.full(0, 0)
		b.u16(0)
		b.u16(0)
		minfHeader = box("smhd", b.bytes())
	case "video":
		var b wbuf
		b.full(0, 1)
		b.u16(0)
		b.zeros(6)
		minfHeader = box("vmhd", b.bytes())
	default:
		var b wbuf
		b.full(0, 0)
		minfHeader = box("nmhd", b.bytes())
	}

	dref := fullbox("dref", 0, 0, func() []byte {
		var b wbuf
		b.u32(1)
		b.raw(fullbox("url ", 0, 1, nil))
		return b.bytes()
	}())
	dinf := container("dinf", dref)

	stts := func() []byte {
		var b wbuf
		b.full(0, 0)
		type run struct{ count, delta uint32 }
		var runs []run
		for _, s := range tr.samples {
			if n := len(runs); n > 0 && runs[n-1].delta == s.delta {
				runs[n-1].count++
			} else {
				runs = append(runs, run{1, s.delta})
			}
		}
		b.u32(uint32(len(runs)))
		for _, r := range runs {
			b.u32(r.count)
			b.u32(r.delta)
		}
		return box("stts", b.bytes())
	}()

	ctts := func() []byte {
		has := false
		for _, s := range tr.samples {
			if s.cts != 0 {
				has = true
			}
		}
		if !has {
			return nil
		}
		var b wbuf
		b.full(1, 0)
		type run struct{ count, off uint32 }
		var runs []run
		for _, s := range tr.samples {
			if n := len(runs); n > 0 && int32(runs[n-1].off) == int32(s.cts) {
				runs[n-1].count++
			} else {
				runs = append(runs, run{1, uint32(int32(s.cts))})
			}
		}
		b.u32(uint32(len(runs)))
		for _, r := range runs {
			b.u32(r.count)
			b.u32(r.off)
		}
		return box("ctts", b.bytes())
	}()

	stsc := func() []byte {
		var b wbuf
		b.full(0, 0)
		b.u32(1)
		b.u32(1)
		b.u32(1)
		b.u32(1)
		return box("stsc", b.bytes())
	}()

	stsz := func() []byte {
		var b wbuf
		b.full(0, 0)
		b.u32(0)
		b.u32(uint32(len(tr.samples)))
		for _, s := range tr.samples {
			b.u32(uint32(len(s.data)))
		}
		return box("stsz", b.bytes())
	}()

	stco := func() []byte {
		var b wbuf
		b.full(0, 0)
		b.u32(uint32(len(offsets)))
		for _, o := range offsets {
			b.u32(uint32(o))
		}
		return box("stco", b.bytes())
	}()

	stss := func() []byte {
		allSync := true
		for _, s := range tr.samples {
			if !s.sync {
				allSync = false
			}
		}
		if allSync {
			return nil
		}
		var b wbuf
		b.full(0, 0)
		var nums []uint32
		for i, s := range tr.samples {
			if s.sync {
				nums = append(nums, uint32(i+1))
			}
		}
		b.u32(uint32(len(nums)))
		for _, n := range nums {
			b.u32(n)
		}
		return box("stss", b.bytes())
	}()

	var stblChildren [][]byte
	stblChildren = append(stblChildren, tr.stsd, stts)
	if ctts != nil {
		stblChildren = append(stblChildren, ctts)
	}
	if stss != nil {
		stblChildren = append(stblChildren, stss)
	}
	stblChildren = append(stblChildren, stsc, stsz, stco)
	stbl := container("stbl", stblChildren...)

	minf := container("minf", minfHeader, dinf, stbl)
	mdia := container("mdia", mdhd, hdlr, minf)
	return container("trak", tkhd, mdia)
}

// emsgFixtureBoxes returns sample-level events only; emsg boxes are written
// into moov here so a remux re-emits them in a stable order.
