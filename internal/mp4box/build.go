// Package mp4box builds and parses minimal MP4 sample-table files using
// github.com/Eyevinn/mp4ff. Only box structure and sample tables are handled;
// no media decoding is performed.
package mp4box

import (
	"bytes"
	"fmt"

	"github.com/Eyevinn/mp4ff/mp4"
)

// SampleTable is the normalized sample description exchanged with the media
// package. It is deliberately codec-agnostic.
type SampleTable struct {
	TrackID         uint32
	Timescale       uint32
	SampleSizes     []uint32
	SampleDeltas    []uint32 // duration delta per sample (stts)
	ChunkOffsets    []uint32 // stco
	SamplesPerChunk uint32   // uniform stsc entry
	Payload         []byte   // mdat payload (synthetic)
}

// Color is the subset of an nclx colr box this package round-trips.
type Color struct {
	Primaries uint16
	Transfer  uint16
	Matrix    uint16
	FullRange bool
}

// BuildFile serializes one sample table plus optional color box into a
// self-contained ftyp+moov+mdat byte stream. Containers are assembled via
// AddChild so mp4ff's typed fields and Children slices stay consistent. The
// output is deterministic for identical inputs (no timestamps, fixed order).
func BuildFile(t SampleTable, col *Color) ([]byte, error) {
	if t.Timescale == 0 {
		return nil, fmt.Errorf("mp4box: timescale must be non-zero")
	}
	ftyp := mp4.NewFtyp("isom", 512, []string{"isom", "iso2", "mp41"})

	stbl := mp4.NewStblBox()
	stsd := mp4.NewStsdBox()
	vse := mp4.CreateVisualSampleEntryBox("avc1", 320, 240, nil)
	if col != nil {
		vse.AddChild(&mp4.ColrBox{
			ColorType:               "nclx",
			ColorPrimaries:          col.Primaries,
			TransferCharacteristics: col.Transfer,
			MatrixCoefficients:      col.Matrix,
			FullRangeFlag:           col.FullRange,
		})
	}
	stsd.AddChild(vse)
	stbl.AddChild(stsd)
	stbl.AddChild(&mp4.SttsBox{
		SampleCount:     []uint32{uint32(len(t.SampleDeltas))},
		SampleTimeDelta: t.SampleDeltas,
	})
	stbl.AddChild(&mp4.StscBox{
		Entries: []mp4.StscEntry{{
			FirstChunk: 1, SamplesPerChunk: t.SamplesPerChunk, FirstSampleNr: 1}},
		SampleDescriptionID: []uint32{1},
	})
	stbl.AddChild(&mp4.StszBox{SampleNumber: uint32(len(t.SampleSizes)), SampleSize: t.SampleSizes})
	stbl.AddChild(&mp4.StcoBox{ChunkOffset: t.ChunkOffsets})

	minf := mp4.NewMinfBox()
	minf.AddChild(mp4.CreateVmhd())
	minf.AddChild(&mp4.DinfBox{Dref: mp4.CreateDref()})
	minf.AddChild(stbl)

	mdia := mp4.NewMdiaBox()
	mdia.AddChild(&mp4.MdhdBox{Timescale: t.Timescale})
	hdlr, err := mp4.CreateHdlr("vide")
	if err != nil {
		return nil, err
	}
	mdia.AddChild(hdlr)
	mdia.AddChild(minf)

	tkhd := mp4.CreateTkhd()
	tkhd.TrackID = t.TrackID
	tkhd.Width = 320 << 16
	tkhd.Height = 240 << 16
	trak := mp4.NewTrakBox()
	trak.AddChild(tkhd)
	trak.AddChild(mdia)

	moov := mp4.NewMoovBox()
	moov.AddChild(mp4.CreateMvhd())
	moov.AddChild(trak)

	mdat := &mp4.MdatBox{Data: t.Payload}
	out := &bytes.Buffer{}
	for _, b := range []mp4.Box{ftyp, moov, mdat} {
		if err := b.Encode(out); err != nil {
			return nil, err
		}
	}
	return out.Bytes(), nil
}
