// Package fixture builds self-contained sample media in memory: a valid
// MP4/fMP4 with multiple timescales and cross-track timed metadata, plus the
// deliberately damaged variants the demo and tests exercise.
package fixture

import "bytes"

// buildFragmented writes ftyp + moov (with mvex/trex) + moof/mdat pairs and a
// mfra/tfra index.
func buildFragmented(tracks []trackSpec, opts Options) []byte {
	ftyp := box("ftyp", append(append([]byte("isom"), []byte{0, 0, 0, 0}...), []byte("isomiso5dash")...))

	var mvexChildren [][]byte
	for _, tr := range tracks {
		var b wbuf
		b.full(0, 0)
		b.u32(tr.id)
		b.u32(1)
		b.u32(tr.samples[0].delta)
		b.u32(uint32(len(tr.samples[0].data)))
		b.u32(0)
		mvexChildren = append(mvexChildren, box("trex", b.bytes()))
	}
	mvex := container("mvex", mvexChildren...)

	var moovChildren [][]byte
	moovChildren = append(moovChildren, mvhdBox(uint32(len(tracks)+1)))
	for _, tr := range tracks {
		frag := tr
		frag.samples = nil
		moovChildren = append(moovChildren, trakBox(frag, nil))
	}
	moovChildren = append(moovChildren, mvex)
	moovChildren = append(moovChildren, emsgFixtureBoxes(opts)...)
	moov := container("moov", moovChildren...)

	var out bytes.Buffer
	out.Write(ftyp)
	out.Write(moov)
	var tfraEntries [][]byte
	seq := uint32(1)
	for _, tr := range tracks {
		// One fragment per track keeps payload ordering independent per track.
		var trunEntries wbuf
		var payload wbuf
		for _, s := range tr.samples {
			trunEntries.u32(s.delta)
			trunEntries.u32(uint32(len(s.data)))
			trunEntries.u32(0)
			payload.raw(s.data)
		}
		var tfhd wbuf
		tfhd.full(0, 0x020000) // default-base-is-moof
		tfhd.u32(tr.id)

		var tfdt wbuf
		tfdt.full(1, 0)
		tfdt.u64(0)

		// The trun data offset is relative to the enclosing moof, so the moof
		// size must be computed before it can be written.
		buildTrun := func(dataOffset int32) []byte {
			var b wbuf
			b.full(0, 0x000001|0x000100|0x000200|0x000400)
			b.u32(uint32(len(tr.samples)))
			b.u32(uint32(dataOffset))
			b.raw(trunEntries.bytes())
			return box("trun", b.bytes())
		}
		moofSize := int64(8 + 16) // moof header + mfhd
		// traf = traf header + tfhd + tfdt + trun
		trafSize := int64(8 + (len(tfhd.bytes()) + 8) + (len(tfdt.bytes()) + 8) + (8 + 4 + 4 + 4 + trunEntries.b.Len()))
		moofSize += trafSize
		dataOffset := int32(moofSize + 8) // mdat header

		var mfhd wbuf
		mfhd.full(0, 0)
		mfhd.u32(seq)
		seq++

		traf := container("traf",
			box("tfhd", tfhd.bytes()),
			box("tfdt", tfdt.bytes()),
			buildTrun(dataOffset),
		)
		moof := container("moof", box("mfhd", mfhd.bytes()), traf)
		mdat := box("mdat", payload.bytes())
		moofOffset := out.Len()
		out.Write(moof)
		out.Write(mdat)

		var entry wbuf
		entry.u64(0)
		entry.u64(uint64(moofOffset))
		entry.u8(1)
		entry.u8(1)
		entry.u8(1)
		tfraEntries = append(tfraEntries, entry.bytes())
	}
	if opts.DropIndex {
		return out.Bytes()
	}
	var tfra wbuf
	tfra.full(1, 0)
	tfra.u32(tracks[0].id)
	tfra.u32(0)
	tfra.u8(0)
	tfra.u8(0)
	tfra.u8(0)
	tfra.u32(uint32(len(tfraEntries)))
	for _, e := range tfraEntries {
		tfra.raw(e)
	}
	mfra := container("mfra", box("tfra", tfra.bytes()))
	out.Write(mfra)
	return out.Bytes()
}
