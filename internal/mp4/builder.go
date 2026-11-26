package mp4

import (
	"bytes"
	"fmt"
)

// BuildFixture builds a small progressive MP4 with one video track. The result
// is deterministic and needs no source media files.
func BuildFixture(spec FixtureSpec) (*BuiltFixture, error) {
	if spec.TrackID == 0 {
		spec.TrackID = 1
	}
	if spec.Timescale == 0 {
		spec.Timescale = 3000
	}
	if spec.SampleCount <= 0 {
		spec.SampleCount = 12
	}
	if spec.SampleSize <= 0 {
		spec.SampleSize = 128
	}
	if spec.MinorVersion == 0 {
		spec.MinorVersion = 512
	}

	sizes := make([]uint32, spec.SampleCount)
	samplePayload := make([][]byte, spec.SampleCount)
	var mdat bytes.Buffer
	for i := 0; i < spec.SampleCount; i++ {
		// Vary sizes while keeping every run deterministic.
		size := spec.SampleSize + (i%4)*32
		b := make([]byte, size)
		b[0] = 'M'
		b[1] = 'P'
		b[2] = '4'
		b[3] = byte(i)
		for j := 4; j < size; j++ {
			b[j] = byte(i*17 + j*31)
		}
		samplePayload[i] = b
		sizes[i] = uint32(size)
		mdat.Write(b)
	}

	duration := uint32(spec.SampleCount) * 3000
	offsets := make([]uint32, spec.SampleCount) // patched after moov size known
	stblChildren := [][]byte{
		stsdBox(),
		sttsBox(spec.SampleCount, 3000),
	}
	if ctts := cttsBox(spec.CompositionOffsets); ctts != nil {
		stblChildren = append(stblChildren, ctts)
	}
	stblChildren = append(stblChildren, stscBox(), stszBox(sizes))
	if stss := stssBox(spec.SyncNumbers); stss != nil {
		stblChildren = append(stblChildren, stss)
	}
	stcoIndex := len(stblChildren)
	stblChildren = append(stblChildren, stcoBox(offsets))
	var stbl bytes.Buffer
	for _, child := range stblChildren {
		stbl.Write(child)
	}
	stblBox := box("stbl", stbl.Bytes())

	var drefPayload bytes.Buffer
	drefPayload.Write(be32(1))
	drefPayload.Write(fullBox("url ", 0, 1, nil))
	dref := fullBox("dref", 0, 0, drefPayload.Bytes())
	var minf bytes.Buffer
	minf.Write(fullBox("vmhd", 0, 1, make([]byte, 8)))
	minf.Write(box("dinf", dref))
	minf.Write(stblBox)
	minfBox := box("minf", minf.Bytes())

	var mdia bytes.Buffer
	mdia.Write(mdhdBox(spec.Timescale, duration))
	mdia.Write(hdlrBox())
	mdia.Write(minfBox)
	mdiaBox := box("mdia", mdia.Bytes())

	elstPayload := make([]byte, 0, 4+12)
	elstPayload = append(elstPayload, be32(1)...)
	elstPayload = append(elstPayload, be32(duration)...)
	elstPayload = append(elstPayload, be32(0)...) // media time
	elstPayload = append(elstPayload, be32(0x00010000)...)
	edtsBox := box("edts", fullBox("elst", 0, 0, elstPayload))

	var trak bytes.Buffer
	trak.Write(tkhdBox(spec.TrackID, duration))
	trak.Write(edtsBox)
	trak.Write(mdiaBox)
	trakBox := box("trak", trak.Bytes())

	var moov bytes.Buffer
	moov.Write(mvhdBox(1000, uint32(spec.SampleCount)*3000/3))
	moov.Write(trakBox)
	moovBox := box("moov", moov.Bytes())

	ftyp := ftypBox(spec.MinorVersion)
	chunkDataStart := len(ftyp) + len(moovBox) + 8
	absOffsets := make([]int, spec.SampleCount)
	pos := chunkDataStart
	for i := range offsets {
		if spec.BadChunkOffset && i == 1 {
			offsets[i] = 0xfffffff0
		} else {
			offsets[i] = uint32(pos)
		}
		absOffsets[i] = pos
		pos += int(sizes[i])
	}

	// Rebuild stco/moov with real offsets.
	stblChildren[stcoIndex] = stcoBox(offsets)
	stbl.Reset()
	for _, child := range stblChildren {
		stbl.Write(child)
	}
	stblBox = box("stbl", stbl.Bytes())
	minf.Reset()
	minf.Write(fullBox("vmhd", 0, 1, make([]byte, 8)))
	minf.Write(box("dinf", dref))
	minf.Write(stblBox)
	minfBox = box("minf", minf.Bytes())
	mdia.Reset()
	mdia.Write(mdhdBox(spec.Timescale, duration))
	mdia.Write(hdlrBox())
	mdia.Write(minfBox)
	mdiaBox = box("mdia", mdia.Bytes())
	trak.Reset()
	trak.Write(tkhdBox(spec.TrackID, duration))
	trak.Write(edtsBox)
	trak.Write(mdiaBox)
	trakBox = box("trak", trak.Bytes())
	moov.Reset()
	moov.Write(mvhdBox(1000, uint32(spec.SampleCount)*3000/3))
	moov.Write(trakBox)
	moovBox = box("moov", moov.Bytes())
	if chunkDataStart != len(ftyp)+len(moovBox)+8 {
		return nil, fmt.Errorf("mp4 fixture: moov size changed during offset patch")
	}

	var out bytes.Buffer
	out.Write(ftyp)
	moovOffset := out.Len()
	out.Write(moovBox)
	out.Write(box("mdat", mdat.Bytes()))
	data := out.Bytes()

	stcoTypeIndex := bytes.Index(data, []byte("stco"))
	if stcoTypeIndex < 4 {
		return nil, fmt.Errorf("mp4 fixture: stco box not found")
	}
	stcoBoxOffset := stcoTypeIndex - 4
	entriesStart := stcoBoxOffset + 8 + 4 + 4 // header + fullbox header + entry count
	return &BuiltFixture{
		Data:             data,
		MoovOffset:       moovOffset,
		STCOBoxOffset:    stcoBoxOffset,
		STCOEntriesStart: entriesStart,
		ChunkDataStart:   chunkDataStart,
		ChunkOffsets:     absOffsets,
	}, nil
}
