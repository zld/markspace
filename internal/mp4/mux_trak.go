package mp4

func buildTrak(b *byteBuilder, tr TrackIn, id, chunkOffset uint32) {
	trak := b.begin("trak")

	tkhd := b.begin("tkhd")
	b.fullBox(0, 3)
	b.u32(0)
	b.u32(0)
	b.u32(id)
	b.u32(0)
	b.u32(uint32(tr.SampleCount) * tr.Delta)
	b.raw(make([]byte, 8))
	b.u16(0) // layer
	b.u16(0) // alternate group
	if tr.Kind == "audio" {
		b.u16(0x0100)
	} else {
		b.u16(0)
	}
	b.u16(0)
	writeIdentityMatrix(b)
	b.u32(uint32(tr.Width) << 16)
	b.u32(uint32(tr.Height) << 16)
	b.end(tkhd)

	mdia := b.begin("mdia")
	mdhd := b.begin("mdhd")
	b.fullBox(0, 0)
	b.u32(0)
	b.u32(0)
	b.u32(tr.Timescale)
	b.u32(uint32(tr.SampleCount) * tr.Delta)
	b.u16(0x55C4) // undetermined language
	b.u16(0)
	b.end(mdhd)

	hdlr := b.begin("hdlr")
	b.fullBox(0, 0)
	b.u32(0)
	if tr.Kind == "audio" {
		b.fourcc("soun")
	} else {
		b.fourcc("vide")
	}
	b.raw(make([]byte, 12))
	b.u8(0) // empty handler name
	b.end(hdlr)

	minf := b.begin("minf")
	if tr.Kind == "audio" {
		smhd := b.begin("smhd")
		b.fullBox(0, 0)
		b.u16(0)
		b.u16(0)
		b.end(smhd)
	} else {
		vmhd := b.begin("vmhd")
		b.fullBox(0, 1)
		b.u16(0)
		b.u16(0)
		b.u16(0)
		b.u16(0)
		b.end(vmhd)
	}

	dinf := b.begin("dinf")
	dref := b.begin("dref")
	b.fullBox(0, 0)
	b.u32(1)
	url := b.begin("url ")
	b.fullBox(0, 1)
	b.end(url)
	b.end(dref)
	b.end(dinf)

	stbl := b.begin("stbl")
	stsd := b.begin("stsd")
	b.fullBox(0, 0)
	b.u32(1)
	writeSampleEntry(b, tr)
	b.end(stsd)

	stts := b.begin("stts")
	b.fullBox(0, 0)
	b.u32(1)
	b.u32(tr.SampleCount)
	b.u32(tr.Delta)
	b.end(stts)

	stsc := b.begin("stsc")
	b.fullBox(0, 0)
	b.u32(1)
	b.u32(1)
	b.u32(tr.SampleCount)
	b.u32(1)
	b.end(stsc)

	stsz := b.begin("stsz")
	b.fullBox(0, 0)
	b.u32(tr.DefaultSampleSize)
	b.u32(tr.SampleCount)
	if tr.DefaultSampleSize == 0 {
		for _, sz := range tr.SampleSizes {
			b.u32(sz)
		}
	}
	b.end(stsz)

	stco := b.begin("stco")
	b.fullBox(0, 0)
	b.u32(1)
	b.u32(chunkOffset)
	b.end(stco)

	if len(tr.SyncSamples) > 0 {
		stss := b.begin("stss")
		b.fullBox(0, 0)
		b.u32(uint32(len(tr.SyncSamples)))
		for _, n := range tr.SyncSamples {
			b.u32(n)
		}
		b.end(stss)
	}

	b.end(stbl)
	b.end(minf)
	b.end(mdia)
	b.end(trak)
}

func writeSampleEntry(b *byteBuilder, tr TrackIn) {
	entry := b.begin(tr.Codec)
	b.raw(make([]byte, 6)) // reserved
	b.u16(1)               // data reference index
	if tr.Kind == "video" {
		b.u16(0) // predefined
		b.u16(0) // reserved
		b.u32(0)
		b.u32(0)
		b.u32(0)
		b.u16(tr.Width)
		b.u16(tr.Height)
		b.u32(0x00480000) // 72 dpi horizontal
		b.u32(0x00480000) // 72 dpi vertical
		b.u32(0)
		b.u16(1) // frame count
		b.raw(make([]byte, 32))
		b.u16(0x0018) // depth
		b.u16(0xFFFF) // predefined -1
	} else {
		b.u64(0) // reserved
		b.u16(tr.Channels)
		b.u16(tr.Bits)
		b.u16(0)
		b.u16(0)
		b.u32(tr.SampleRate << 16)
	}
	b.end(entry)
}
