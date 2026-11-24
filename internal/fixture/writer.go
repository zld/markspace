package fixture

import (
	"os"

	mp4 "github.com/abema/go-mp4"
)

type boxWriter struct {
	w   *mp4.Writer
	f   *os.File
	err error
}

func (bw *boxWriter) start(typ string) *mp4.BoxInfo {
	if bw.err != nil {
		return &mp4.BoxInfo{}
	}
	bi, err := bw.w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(typ)})
	if err != nil {
		bw.err = err
		return &mp4.BoxInfo{}
	}
	return bi
}

func (bw *boxWriter) end() *mp4.BoxInfo {
	if bw.err != nil {
		return &mp4.BoxInfo{}
	}
	bi, err := bw.w.EndBox()
	if err != nil {
		bw.err = err
	}
	return bi
}

func filePos(f *os.File) uint64 {
	pos, err := f.Seek(0, 1)
	if err != nil {
		return 0
	}
	return uint64(pos)
}

func writeFile(path string, t *Table) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	bw := &boxWriter{w: mp4.NewWriter(f), f: f}
	spec := t.Spec
	ctx := mp4.Context{}

	bw.start("ftyp")
	if _, err := mp4.Marshal(bw.w, &mp4.Ftyp{
		MajorBrand: [4]byte{'i', 's', 'o', 'm'}, MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '2'}},
			{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}},
		},
	}, ctx); err != nil {
		return err
	}
	bw.end()

	bw.start("moov")
	bw.start("mvhd")
	if _, err := mp4.Marshal(bw.w, &mp4.Mvhd{
		Timescale: t.MovieTimescale, DurationV0: uint32(t.EditMovieDur),
		Rate: 0x10000, Volume: 0x100,
		Matrix:      [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		NextTrackID: 2,
	}, ctx); err != nil {
		return err
	}
	bw.end()

	bw.start("trak")
	bw.start("tkhd")
	if _, err := mp4.Marshal(bw.w, &mp4.Tkhd{
		FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 3}},
		TrackID: 1, DurationV0: uint32(t.EditMovieDur), Volume: 0x100,
		Matrix: [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		Width:  uint32(spec.Width) << 16, Height: uint32(spec.Height) << 16,
	}, ctx); err != nil {
		return err
	}
	bw.end()

	bw.start("edts")
	bw.start("elst")
	entries := []mp4.ElstEntry{
		{SegmentDurationV0: uint32(t.PressStartMs), MediaTimeV0: -1, MediaRateInteger: 1},
		{SegmentDurationV0: uint32(t.EditMovieDur), MediaTimeV0: int32(t.EditMediaTime), MediaRateInteger: 1},
	}
	if _, err := mp4.Marshal(bw.w, &mp4.Elst{EntryCount: uint32(len(entries)), Entries: entries}, ctx); err != nil {
		return err
	}
	bw.end()
	bw.end()

	bw.start("mdia")
	bw.start("mdhd")
	if _, err := mp4.Marshal(bw.w, &mp4.Mdhd{
		Timescale: spec.Timescale, DurationV0: uint32(t.MediaTicks),
		Language: [3]byte{21, 14, 4},
	}, ctx); err != nil {
		return err
	}
	bw.end()
	bw.start("hdlr")
	if _, err := mp4.Marshal(bw.w, &mp4.Hdlr{
		HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "VideoHandler",
	}, ctx); err != nil {
		return err
	}
	bw.end()

	bw.start("minf")
	bw.start("vmhd")
	if _, err := mp4.Marshal(bw.w, &mp4.Vmhd{}, ctx); err != nil {
		return err
	}
	bw.end()
	bw.start("dinf")
	bw.start("dref")
	if _, err := mp4.Marshal(bw.w, &mp4.Dref{EntryCount: 1}, ctx); err != nil {
		return err
	}
	bw.start("url ")
	if _, err := mp4.Marshal(bw.w, &mp4.Url{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}, ctx); err != nil {
		return err
	}
	bw.end()
	bw.end()
	bw.end()

	bw.start("stbl")
	bw.start("stsd")
	if _, err := mp4.Marshal(bw.w, &mp4.Stsd{EntryCount: 1}, ctx); err != nil {
		return err
	}
	if err := writeVideoSampleEntry(bw, spec); err != nil {
		return err
	}
	bw.end()

	bw.start("stts")
	stts := sttsEntries(len(t.Samples), spec.Delta(), spec.StuckTail)
	if _, err := mp4.Marshal(bw.w, &mp4.Stts{EntryCount: uint32(len(stts)), Entries: stts}, ctx); err != nil {
		return err
	}
	bw.end()

	if !spec.MissingSync {
		bw.start("stss")
		syncs := syncSampleNumbers(t.Samples)
		if _, err := mp4.Marshal(bw.w, &mp4.Stss{EntryCount: uint32(len(syncs)), SampleNumber: syncs}, ctx); err != nil {
			return err
		}
		bw.end()
	}

	bw.start("stsz")
	sizes := make([]uint32, len(t.Samples))
	for i, s := range t.Samples {
		sizes[i] = s.Size
	}
	if _, err := mp4.Marshal(bw.w, &mp4.Stsz{SampleSize: 0, SampleCount: uint32(len(sizes)), EntrySize: sizes}, ctx); err != nil {
		return err
	}
	bw.end()

	bw.start("stsc")
	if _, err := mp4.Marshal(bw.w, &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{
		FirstChunk: 1, SamplesPerChunk: uint32(len(t.Samples)), SampleDescriptionIndex: 1,
	}}}, ctx); err != nil {
		return err
	}
	bw.end()

	bw.start("stco")
	offs := make([]uint32, len(t.Samples))
	for i, s := range t.Samples {
		offs[i] = uint32(s.Offset)
	}
	if _, err := mp4.Marshal(bw.w, &mp4.Stco{EntryCount: uint32(len(offs)), ChunkOffset: offs}, ctx); err != nil {
		return err
	}
	bw.end()
	bw.end()
	bw.end()
	bw.end()
	bw.end()
	bw.end()

	bw.start("mdat")
	t.MdatOffset = filePos(f)
	payload := make([]byte, 4096)
	for i, s := range t.Samples {
		fillPayload(payload, spec.Seed, i)
		n := int(s.Size)
		for n > 0 {
			chunk := n
			if chunk > len(payload) {
				chunk = len(payload)
			}
			if _, err := f.Write(payload[:chunk]); err != nil {
				return err
			}
			n -= chunk
		}
	}
	if bx := bw.end(); bx.Size > 0 {
		t.MdatSize = bx.Size
	}

	if bw.err != nil {
		return bw.err
	}
	return f.Sync()
}

// fillPayload fills buf deterministically from seed and sample index.
func fillPayload(buf []byte, seed int64, index int) {
	x := uint64(seed)*0x9E3779B97F4A7C15 + uint64(index+1)*0xBF58476D1CE4E5B9
	for i := range buf {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		buf[i] = byte(x >> 32)
	}
}

func writeVideoSampleEntry(bw *boxWriter, spec Spec) error {
	ctx := mp4.Context{}
	bw.start("avc1")
	vse := &mp4.VisualSampleEntry{
		SampleEntry:     mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.StrToBoxType("avc1")}, DataReferenceIndex: 1},
		Width:           spec.Width,
		Height:          spec.Height,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	if _, err := mp4.Marshal(bw.w, vse, ctx); err != nil {
		return err
	}
	bw.start("avcC")
	avcc := &mp4.AVCDecoderConfiguration{
		AnyTypeBox:                 mp4.AnyTypeBox{Type: mp4.StrToBoxType("avcC")},
		ConfigurationVersion:       1,
		Profile:                    0x64,
		Level:                      0x28,
		LengthSizeMinusOne:         3,
		NumOfSequenceParameterSets: 1,
		SequenceParameterSets:      []mp4.AVCParameterSet{{Length: 4, NALUnit: []byte{0x67, 0x64, 0x00, 0x28}}},
		NumOfPictureParameterSets:  1,
		PictureParameterSets:       []mp4.AVCParameterSet{{Length: 4, NALUnit: []byte{0x68, 0xEE, 0x3C, 0x80}}},
	}
	if _, err := mp4.Marshal(bw.w, avcc, ctx); err != nil {
		return err
	}
	bw.end()
	bw.end()
	return nil
}
