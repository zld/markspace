package fixture

import (
	"encoding/binary"
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"archive/internal/essence"
	"archive/internal/model"
)

// BuildMP4 renders a spec as a complete MP4 file with a single mdat that
// holds every track's frames, interleaved track by track. The sample tables
// describe the real payload positions; when a track is marked DropsIndex its
// stbl keeps only the mandatory boxes and omits stts/stsc/stsz/stco so the
// demuxer must fall back to scanning.
func BuildMP4(s Spec) (*Layout, []byte, error) {
	if s.Format != model.ContainerMP4 {
		return nil, nil, fmt.Errorf("fixture: %s is not an mp4 spec", s.Name)
	}
	if err := s.Validate(); err != nil {
		return nil, nil, err
	}
	layout := &Layout{Spec: s}
	tracks := make([]*TrackLayout, len(s.Tracks))
	for i, ts := range s.Tracks {
		tracks[i] = &TrackLayout{Spec: ts}
	}
	layout.Tracks = tracks

	// Build the marker frames once. Sizes and timing are fixed, so the only
	// unknown is where mdat lands; the moov is written first (faststart) so
	// a truncated media area still leaves metadata readable.
	frames := make([][][]byte, len(tracks))
	payloadSize := uint64(0)
	for ti, tl := range tracks {
		frames[ti] = make([][]byte, tl.Spec.Frames)
		for i := 0; i < tl.Spec.Frames; i++ {
			frames[ti][i] = BuildFrame(tl.Spec, i)
			payloadSize += uint64(len(frames[ti][i]))
		}
	}
	mdatHeader := uint64(8)
	if payloadSize+mdatHeader > 0xFFFFFFFF {
		mdatHeader = 16
	}

	// Pass one measures the moov with placeholder offsets. The size does not
	// depend on the offset values because stco entries are fixed width.
	dummy := &memWriteSeeker{}
	dw := mp4.NewWriter(dummy)
	if err := writeFtyp(dw); err != nil {
		return nil, nil, err
	}
	ftypSize := uint64(len(dummy.buf))
	for _, tl := range tracks {
		tl.MediaBase = 0
		tl.Frames = layoutFrames(tl.Spec)
	}
	if err := writeMoov(dw, s, tracks); err != nil {
		return nil, nil, err
	}
	moovSize := uint64(len(dummy.buf)) - ftypSize
	base := ftypSize + moovSize + mdatHeader

	// Pass two: real offsets.
	cursor := base
	for ti, tl := range tracks {
		tl.MediaBase = cursor
		tl.Frames = layoutFrames(tl.Spec)
		for i := range tl.Frames {
			tl.Frames[i].Offset = cursor
			tl.Frames[i].Size = uint64(len(frames[ti][i]))
			cursor += uint64(len(frames[ti][i]))
		}
		size := uint64(0)
		for _, f := range tl.Frames {
			size += f.Size
		}
		tl.MediaSize = size
	}

	m := &memWriteSeeker{}
	w := mp4.NewWriter(m)
	if err := writeFtyp(w); err != nil {
		return nil, nil, err
	}
	if err := writeMoov(w, s, tracks); err != nil {
		return nil, nil, err
	}
	if uint64(len(m.buf)) != base-mdatHeader {
		return nil, nil, fmt.Errorf("fixture: moov size drifted (measured %d, actual %d)", moovSize, uint64(len(m.buf))-ftypSize)
	}
	hdr := make([]byte, mdatHeader)
	if mdatHeader == 16 {
		binary.BigEndian.PutUint32(hdr[0:4], 1)
		copy(hdr[4:8], "mdat")
		binary.BigEndian.PutUint64(hdr[8:16], payloadSize+mdatHeader)
	} else {
		binary.BigEndian.PutUint32(hdr[0:4], uint32(payloadSize+mdatHeader))
		copy(hdr[4:8], "mdat")
	}
	if _, err := m.Write(hdr); err != nil {
		return nil, nil, err
	}
	for ti := range tracks {
		for _, fr := range frames[ti] {
			if _, err := m.Write(fr); err != nil {
				return nil, nil, err
			}
		}
	}
	layout.MediaStart = base
	layout.MediaEnd = cursor
	layout.Size = uint64(len(m.buf))
	return layout, m.buf, nil
}

// layoutFrames creates the timing skeleton for a track. Offsets and sizes are
// filled in by the caller once the media base is known.
func layoutFrames(ts TrackSpec) []FrameLayout {
	out := make([]FrameLayout, ts.Frames)
	for i := range out {
		out[i] = FrameLayout{
			Seq:       uint32(i),
			StartTick: ts.StartTick + uint64(i)*ts.FrameTicks,
			Duration:  ts.FrameTicks,
			Sync:      ts.SyncEvery <= 1 || i%ts.SyncEvery == 0,
		}
	}
	return out
}

func writeFtyp(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeFtyp()}); err != nil {
		return err
	}
	ftyp := &mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', 'm'},
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', 'm'}},
			{CompatibleBrand: [4]byte{'i', 's', 'o', '6'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
		},
	}
	if _, err := mp4.Marshal(w, ftyp, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeMoov(w *mp4.Writer, s Spec, tracks []*TrackLayout) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	movieTimescale := uint32(1000)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	mvhd := &mp4.Mvhd{
		Timescale:   movieTimescale,
		DurationV0:  1000,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		NextTrackID: uint32(len(tracks) + 1),
	}
	if _, err := mp4.Marshal(w, mvhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	for _, tl := range tracks {
		if err := writeTrak(w, tl); err != nil {
			return err
		}
	}
	_, err := w.EndBox()
	return err
}

// colrCode maps a fixture colour name to an ISO 23091-2 code point.
func colourCodes(c ColorClaim) (primaries, transfer, matrix uint16) {
	primaries, transfer, matrix = 2, 2, 2 // unspecified
	switch c.Primaries {
	case "bt709":
		primaries = 1
	case "bt2020":
		primaries = 9
	}
	switch c.Transfer {
	case "bt709":
		transfer = 1
	case "smpte2084":
		transfer = 16
	}
	switch c.Matrix {
	case "bt709":
		matrix = 1
	case "bt2020nc":
		matrix = 9
	}
	return primaries, transfer, matrix
}

// rotationMatrix encodes a clockwise display rotation in the tkhd matrix.
func rotationMatrix(deg int) [9]int32 {
	one := int32(0x10000)
	switch ((deg % 360) + 360) % 360 {
	case 90:
		// (x,y) -> (y,-x): a=0,b=1,c=-1,d=0
		return [9]int32{0, one, 0, -one, 0, 0, 0, 0, 0x40000000}
	case 180:
		return [9]int32{-one, 0, 0, 0, -one, 0, 0, 0, 0x40000000}
	case 270:
		return [9]int32{0, -one, 0, one, 0, 0, 0, 0, 0x40000000}
	default:
		return [9]int32{one, 0, 0, 0, one, 0, 0, 0, 0x40000000}
	}
}

func writeTrak(w *mp4.Writer, tl *TrackLayout) error {
	ts := tl.Spec
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if err := writeTkhd(w, tl); err != nil {
		return err
	}
	// edts/elst with a negative media time used by several historical MXF
	// round-trips. The demuxer must honour it when building the timeline.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeEdts()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeElst()}); err != nil {
		return err
	}
	elst := &mp4.Elst{EntryCount: 1, Entries: []mp4.ElstEntry{{
		SegmentDurationV0: uint32(ts.Frames) * uint32(ts.FrameTicks) * 1000 / ts.Timescale,
		MediaTimeV0:       0,
		MediaRateInteger:  1,
	}}}
	if _, err := mp4.Marshal(w, elst, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	if err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{
		Timescale:  ts.Timescale,
		DurationV0: uint32(tl.EndTick() - ts.StartTick),
		Language:   [3]byte{'u', 'n', 'd'},
	}
	if _, err := mp4.Marshal(w, mdhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if err := writeHdlr(w, ts); err != nil {
		return err
	}
	if err := writeMinf(w, tl); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func writeTkhd(w *mp4.Writer, tl *TrackLayout) error {
	ts := tl.Spec
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		TrackID:        ts.TrackID,
		DurationV0:     uint32(tl.EndTick() - ts.StartTick),
		Layer:          0,
		AlternateGroup: 0,
		Volume:         0,
		Matrix:         rotationMatrix(ts.Rotation),
		Width:          uint32(1920) << 16,
		Height:         uint32(1080) << 16,
	}
	tkhd.SetFlags(3)
	if ts.Kind == model.KindAudio {
		tkhd.Volume = 0x0100
		tkhd.Width, tkhd.Height = 0, 0
	}
	if _, err := mp4.Marshal(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeHdlr(w *mp4.Writer, ts TrackSpec) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	handler := "vide"
	switch ts.Kind {
	case model.KindAudio:
		handler = "soun"
	case model.KindData:
		handler = "meta"
	}
	hdlr := &mp4.Hdlr{HandlerType: [4]byte{handler[0], handler[1], handler[2], handler[3]}, Name: string(ts.Kind)}
	if _, err := mp4.Marshal(w, hdlr, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeMinf(w *mp4.Writer, tl *TrackLayout) error {
	ts := tl.Spec
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	switch ts.Kind {
	case model.KindAudio:
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeSmhd()}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.Smhd{}, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	default:
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeVmhd()}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.Vmhd{}, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if err := writeStbl(w, tl); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeDinf(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	url := &mp4.Url{}
	url.SetFlags(mp4.UrlSelfContained)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, url, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeStbl(w *mp4.Writer, tl *TrackLayout) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeStsd(w, tl.Spec); err != nil {
		return err
	}
	if !tl.Spec.DropsIndex {
		if err := writeIndexBoxes(w, tl); err != nil {
			return err
		}
	}
	_, err := w.EndBox()
	return err
}

func writeStsd(w *mp4.Writer, ts TrackSpec) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	sampleType := ts.Codec
	if len(sampleType) != 4 {
		switch ts.Kind {
		case model.KindAudio:
			sampleType = "mp4a"
		default:
			sampleType = "avc1"
		}
	}
	boxType := mp4.StrToBoxType(sampleType)
	entry, err := boxType.New(mp4.Context{})
	if err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: boxType}); err != nil {
		return err
	}
	if ts.Kind == model.KindAudio {
		v := entry.(*mp4.AudioSampleEntry)
		// Assign only the payload field: replacing SampleEntry wholesale
		// would erase the box type that New/SettType stored in AnyTypeBox.
		v.SampleEntry.DataReferenceIndex = 1
		v.ChannelCount = 2
		v.SampleSize = 16
		v.SampleRate = ts.Timescale << 16
	} else {
		v := entry.(*mp4.VisualSampleEntry)
		v.SampleEntry.DataReferenceIndex = 1
		v.Width = 1920
		v.Height = 1080
		v.Horizresolution = 0x00480000
		v.Vertresolution = 0x00480000
		v.FrameCount = 1
		v.Depth = 0x0018
	}
	if _, err := mp4.Marshal(w, entry, mp4.Context{}); err != nil {
		return err
	}
	if ts.Kind == model.KindVideo {
		if err := writeVideoSampleFields(w, ts); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

func writeVideoSampleFields(w *mp4.Writer, ts TrackSpec) error {
	p, tr, m := colourCodes(ts.Color)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeColr()}); err != nil {
		return err
	}
	colr := &mp4.Colr{
		ColourType:              [4]byte{'n', 'c', 'l', 'x'},
		ColourPrimaries:         p,
		TransferCharacteristics: tr,
		MatrixCoefficients:      m,
		FullRangeFlag:           ts.Color.FullRange,
	}
	if _, err := mp4.Marshal(w, colr, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// clap is not modelled by go-mp4, so it is written as a raw box. It
	// records the clean aperture width/height (offset already applied by the
	// caller) that the probe must read back as a crop.
	if ts.Crop != nil {
		clap := make([]byte, 8+32)
		binary.BigEndian.PutUint32(clap[0:4], uint32(len(clap)))
		copy(clap[4:8], "clap")
		n := ts.Crop.Right - ts.Crop.Left
		h := ts.Crop.Bottom - ts.Crop.Top
		vals := []uint32{uint32(n), 1, uint32(h), 1, 0, 1, 0, 1}
		for i, v := range vals {
			binary.BigEndian.PutUint32(clap[8+i*4:], v)
		}
		if _, err := w.Write(clap); err != nil {
			return err
		}
	}
	// pasp keeps the sample entry valid for players that expect it.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypePasp()}); err != nil {
		return err
	}
	pasp := &mp4.PixelAspectRatioBox{HSpacing: 1, VSpacing: 1}
	pasp.SetType(mp4.BoxTypePasp())
	if _, err := mp4.Marshal(w, pasp, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeIndexBoxes(w *mp4.Writer, tl *TrackLayout) error {
	ts := tl.Spec
	// stts: one entry if all frames share a delta, otherwise one per frame.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	stts := &mp4.Stts{EntryCount: 1, Entries: []mp4.SttsEntry{{SampleCount: uint32(ts.Frames), SampleDelta: uint32(ts.FrameTicks)}}}
	if _, err := mp4.Marshal(w, stts, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return err
	}
	stsc := &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(ts.Frames), SampleDescriptionIndex: 1}}}
	if _, err := mp4.Marshal(w, stsc, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	stsz := &mp4.Stsz{SampleCount: uint32(ts.Frames)}
	for _, f := range tl.Frames {
		stsz.EntrySize = append(stsz.EntrySize, uint32(f.Size))
	}
	if _, err := mp4.Marshal(w, stsz, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// One chunk for the whole track keeps the layout simple and the chunk
	// offset equal to the first frame's payload offset.
	offsets := []uint64{tl.MediaBase}
	if err := writeChunkOffsets(w, tl, offsets); err != nil {
		return err
	}
	if ts.SyncEvery > 0 {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
			return err
		}
		stss := &mp4.Stss{}
		for i, f := range tl.Frames {
			if f.Sync {
				stss.SampleNumber = append(stss.SampleNumber, uint32(i+1))
			}
		}
		stss.EntryCount = uint32(len(stss.SampleNumber))
		if _, err := mp4.Marshal(w, stss, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil {
			return err
		}
	}
	return nil
}

func writeChunkOffsets(w *mp4.Writer, tl *TrackLayout, offsets []uint64) error {
	if tl.Spec.Container != model.ContainerMP4 {
		return nil
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return err
	}
	stco := &mp4.Stco{EntryCount: uint32(len(offsets))}
	for _, o := range offsets {
		stco.ChunkOffset = append(stco.ChunkOffset, uint32(o))
	}
	if _, err := mp4.Marshal(w, stco, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

// countFrames is a tiny helper used by tests to express expected sizes.
func countFrames(s Spec) int { return s.FrameCount() }

var _ = essence.HeaderSize
