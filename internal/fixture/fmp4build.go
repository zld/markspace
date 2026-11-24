package fixture

// FragmentSpec describes one movie fragment. BaseTime is the raw uint32
// tfdt version-0 value (it may intentionally have wrapped).
type FragmentSpec struct {
	Sequence uint32
	BaseTime uint32
	Deltas   []uint32
	Sizes    []uint32
	Seed     uint64
}

// BuildFragmentedInit builds an fMP4 initialization segment: ftyp + moov with
// an stsd sample entry and an mvex/trex default table.
func BuildFragmentedInit(timescale uint32) []byte {
	ftyp := buildFtyp()

	var mv wb
	mv.u32(0)
	mv.u32(0)
	mv.u32(timescale)
	mv.u32(0)
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
	tk.u32(0)
	tk.p(8)
	tk.u16(0)
	tk.u16(0)
	tk.u16(0)
	tk.u16(0)
	identityMatrix(&tk)
	tk.u32(320 << 16)
	tk.u32(240 << 16)
	tkhd := fullBox("tkhd", 0, 0x000007, tk.b)

	var md wb
	md.u32(0)
	md.u32(0)
	md.u32(timescale)
	md.u32(0)
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
	av.u16(320)
	av.u16(240)
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
	stts := fullBox("stts", 0, 0, u32be(0))
	stsc := fullBox("stsc", 0, 0, u32be(0))
	stsz := fullBox("stsz", 0, 0, concat(u32be(0), u32be(0)))
	stbl := box("stbl", concat(stsd, stts, stsc, stsz))

	minf := box("minf", concat(vmhd, dinf, stbl))
	mdia := box("mdia", concat(mdhd, hdlr, minf))
	trak := box("trak", concat(tkhd, mdia))

	var tx wb
	tx.u32(1) // track id
	tx.u32(1) // sample description index
	tx.u32(0) // default duration
	tx.u32(0) // default size
	tx.u32(0) // default flags
	trex := fullBox("trex", 0, 0, tx.b)
	mvex := box("mvex", trex)

	moov := box("moov", concat(mvhd, trak, mvex))
	return concat(ftyp, moov)
}

// BuildMediaFragment serializes one moof+mdat pair.
func BuildMediaFragment(spec FragmentSpec) []byte {
	var mf wb
	mf.u32(spec.Sequence)
	mfhd := fullBox("mfhd", 0, 0, mf.b)

	var th wb
	th.u32(1) // track id
	tfhd := fullBox("tfhd", 0, 0x020000 /* default-base-is-moof */, th.b)

	var td wb
	td.u32(spec.BaseTime)
	tfdt := fullBox("tfdt", 0, 0, td.b)

	sampleCount := len(spec.Deltas)
	var tr0 wb
	tr0.u32(uint32(sampleCount))
	tr0.i32(0) // placeholder data offset, fixed width
	for i := 0; i < sampleCount; i++ {
		tr0.u32(spec.Deltas[i])
		tr0.u32(spec.Sizes[i])
	}
	trun0 := fullBox("trun", 0, 0x000001|0x000100|0x000200, tr0.b)
	moofSize := 8 + len(box("traf", concat(tfhd, tfdt, trun0))) + 8 // moof header

	var tr wb
	tr.u32(uint32(sampleCount))
	tr.i32(int32(moofSize + 8)) // relative to moof start -> mdat payload
	for i := 0; i < sampleCount; i++ {
		tr.u32(spec.Deltas[i])
		tr.u32(spec.Sizes[i])
	}
	trun := fullBox("trun", 0, 0x000001|0x000100|0x000200, tr.b)
	traf := box("traf", concat(tfhd, tfdt, trun))
	moof := box("moof", concat(mfhd, traf))

	payload := []byte{}
	for i, z := range spec.Sizes {
		payload = append(payload, deterministicPayload(int(z), spec.Seed+uint64(i)*40503)...)
	}
	mdat := box("mdat", payload)
	return concat(moof, mdat)
}
