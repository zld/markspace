package fixture

// SampleSpec describes one encoded sample.
type SampleSpec struct {
	Delta uint32
	Size  int
	Sync  bool
}

// BuiltSample records ground-truth location of one generated sample.
type BuiltSample struct {
	Index          int
	Offset         int64 // absolute byte offset in the MP4 file
	Size           int
	Delta          uint32
	TimeStartTicks int64 // in MediaSpec.Timescale
	Sync           bool
}

// BuiltMedia is a generated progressive MP4 plus its ground truth.
type BuiltMedia struct {
	Bytes     []byte
	Timescale uint32
	Samples   []BuiltSample
}

// ProgressiveSpec configures BuildProgressive.
type ProgressiveSpec struct {
	Timescale uint32
	Samples   []SampleSpec
	Seed      uint64
	Width     uint16
	Height    uint16
}

// BuildProgressive serializes a minimal but structurally valid progressive
// MP4 with all samples in one chunk and one stsc entry.
func BuildProgressive(spec ProgressiveSpec) (*BuiltMedia, error) {
	ts := spec.Timescale
	if ts == 0 {
		ts = 90000
	}
	w, h := spec.Width, spec.Height
	if w == 0 {
		w, h = 320, 240
	}
	n := len(spec.Samples)

	var duration int64
	syncNums := []uint32{}
	payload := []byte{}
	sizes := make([]uint32, n)
	for i, s := range spec.Samples {
		if s.Sync {
			syncNums = append(syncNums, uint32(i+1))
		}
		sizes[i] = uint32(s.Size)
		payload = append(payload, deterministicPayload(s.Size, spec.Seed+uint64(i)*2654435761)...)
		duration += int64(s.Delta)
	}

	ftyp := buildFtyp()
	moov0 := buildMoov(ts, uint32(duration), n, spec.Samples, sizes, syncNums, 0, w, h)
	chunkOffset := uint32(len(ftyp) + len(moov0) + 8) // + mdat header
	moov := buildMoov(ts, uint32(duration), n, spec.Samples, sizes, syncNums, chunkOffset, w, h)
	mdat := box("mdat", payload)

	out := append([]byte{}, ftyp...)
	out = append(out, moov...)
	out = append(out, mdat...)

	built := &BuiltMedia{Bytes: out, Timescale: ts}
	off := int64(chunkOffset)
	var t int64
	for i, s := range spec.Samples {
		built.Samples = append(built.Samples, BuiltSample{
			Index: i, Offset: off, Size: s.Size, Delta: s.Delta,
			TimeStartTicks: t, Sync: s.Sync,
		})
		off += int64(s.Size)
		t += int64(s.Delta)
	}
	return built, nil
}

func buildMoov(ts uint32, duration uint32, n int, samples []SampleSpec, sizes, syncNums []uint32, chunkOffset uint32, width, height uint16) []byte {
	var mv wb
	mv.u32(0)
	mv.u32(0)
	mv.u32(ts)
	mv.u32(duration)
	mv.u32(0x00010000)
	mv.u16(0x0100)
	mv.p(10)
	identityMatrix(&mv)
	mv.p(24)
	mv.u32(2)
	mvhd := fullBox("mvhd", 0, 0, mv.b)

	var tk wb
	tk.u32(0)
	tk.u32(0)
	tk.u32(1)
	tk.u32(0)
	tk.u32(duration)
	tk.p(8)
	tk.u16(0)
	tk.u16(0)
	tk.u16(0)
	tk.u16(0)
	identityMatrix(&tk)
	tk.u32(uint32(width) << 16)
	tk.u32(uint32(height) << 16)
	tkhd := fullBox("tkhd", 0, 0x000007, tk.b)

	var md wb
	md.u32(0)
	md.u32(0)
	md.u32(ts)
	md.u32(duration)
	md.u16(0)
	md.u16(0)
	mdhd := fullBox("mdhd", 0, 0, md.b)

	var hl wb
	hl.u32(0)
	hl.fourcc("vide")
	hl.p(12)
	hl.raw([]byte("VideoHandler\x00"))
	hdlr := fullBox("hdlr", 0, 0, hl.b)

	var vm wb
	vm.u16(0)
	vm.u16(0)
	vm.u16(0)
	vm.u16(0)
	vmhd := fullBox("vmhd", 0, 1, vm.b)

	url := fullBox("url ", 0, 1, nil)
	var dr wb
	dr.u32(1)
	dr.raw(url)
	dref := fullBox("dref", 0, 0, dr.b)
	dinf := box("dinf", dref)

	var av wb
	av.p(6)
	av.u16(1)
	av.u16(0)
	av.u16(0)
	av.u32(0)
	av.u32(0)
	av.u32(0)
	av.u16(width)
	av.u16(height)
	av.u32(0x00480000)
	av.u32(0x00480000)
	av.u32(0)
	av.u16(1)
	av.raw(make([]byte, 32))
	av.u16(0x0018)
	av.i32(-1)
	avc1 := box("avc1", av.b)

	var sd wb
	sd.u32(1)
	sd.raw(avc1)
	stsd := fullBox("stsd", 0, 0, sd.b)

	var st wb
	st.u32(uint32(n))
	for _, s := range samples {
		st.u32(1)
		st.u32(s.Delta)
	}
	stts := fullBox("stts", 0, 0, st.b)

	stblChildren := concat(stsd, stts)
	if len(syncNums) > 0 {
		var ss wb
		ss.u32(uint32(len(syncNums)))
		for _, num := range syncNums {
			ss.u32(num)
		}
		stblChildren = append(stblChildren, fullBox("stss", 0, 0, ss.b)...)
	}

	var sc wb
	sc.u32(1)
	sc.u32(1)
	sc.u32(uint32(n))
	sc.u32(1)
	stsc := fullBox("stsc", 0, 0, sc.b)

	var sz wb
	sz.u32(0)
	sz.u32(uint32(n))
	for _, z := range sizes {
		sz.u32(z)
	}
	stsz := fullBox("stsz", 0, 0, sz.b)

	var co wb
	co.u32(1)
	co.u32(chunkOffset)
	stco := fullBox("stco", 0, 0, co.b)

	stblChildren = append(stblChildren, stsc...)
	stblChildren = append(stblChildren, stsz...)
	stblChildren = append(stblChildren, stco...)
	stbl := box("stbl", stblChildren)

	minf := box("minf", concat(vmhd, dinf, stbl))
	mdia := box("mdia", concat(mdhd, hdlr, minf))
	trak := box("trak", concat(tkhd, mdia))
	return box("moov", concat(mvhd, trak))
}
