package remux

import (
	"encoding/binary"
	"errors"
	"io"

	mp4 "github.com/abema/go-mp4"

	"archive/internal/model"
)

// memWriteSeeker is the minimal io.WriteSeeker the mp4 writer needs while the
// box tree is assembled in memory.
type memWriteSeeker struct {
	buf []byte
	pos int64
}

var errNegativeSeek = errors.New("remux: negative seek")

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	end := m.pos + int64(len(p))
	if end < 0 {
		return 0, errNegativeSeek
	}
	for int64(len(m.buf)) < end {
		m.buf = append(m.buf, 0)
	}
	copy(m.buf[m.pos:], p)
	m.pos = end
	return len(p), nil
}

func (m *memWriteSeeker) Seek(offset int64, whence int) (int64, error) {
	var base int64
	switch whence {
	case io.SeekStart:
		base = 0
	case io.SeekCurrent:
		base = m.pos
	case io.SeekEnd:
		base = int64(len(m.buf))
	default:
		return 0, errNegativeSeek
	}
	next := base + offset
	if next < 0 {
		return 0, errNegativeSeek
	}
	m.pos = next
	return next, nil
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

// writeMoov emits a faststart movie describing exactly the committed
// fragments. Chunk offsets are absolute file offsets of each sample. The
// mediaBase argument is the first essence byte; when it is 0 the moov is a
// size-measuring pass and the offsets are zero.
func writeMoov(w *mp4.Writer, tracks []*TrackSource, byKey map[string]*TrackResult, mediaBase uint64) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return err
	}
	mvhd := &mp4.Mvhd{
		Timescale:   1000,
		DurationV0:  uint32(1000),
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
	for _, t := range tracks {
		tr := byKey[t.Key]
		if tr == nil || len(tr.Committed) == 0 {
			continue
		}
		if err := writeTrak(w, t, tr, mediaBase); err != nil {
			return err
		}
	}
	_, err := w.EndBox()
	return err
}

func writeTrak(w *mp4.Writer, t *TrackSource, tr *TrackResult, mediaBase uint64) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		TrackID:    t.TrackID,
		DurationV0: uint32(tr.EndNS / 1_000_000),
		Matrix:     rotationMatrix(t.Rotation),
	}
	tkhd.SetFlags(3)
	if t.Kind == model.KindVideo {
		tkhd.Width = 1920 << 16
		tkhd.Height = 1080 << 16
	} else {
		tkhd.Volume = 0x0100
	}
	if _, err := mp4.Marshal(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return err
	}
	duration := uint64(0)
	for _, cf := range tr.Committed {
		duration += cf.Duration
	}
	mdhd := &mp4.Mdhd{
		Timescale:  t.Timebase.Timescale,
		DurationV0: uint32(duration),
		Language:   [3]byte{'u', 'n', 'd'},
	}
	if _, err := mp4.Marshal(w, mdhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return err
	}
	handler := "meta"
	switch t.Kind {
	case model.KindVideo:
		handler = "vide"
	case model.KindAudio:
		handler = "soun"
	}
	hdlr := &mp4.Hdlr{HandlerType: [4]byte{handler[0], handler[1], handler[2], handler[3]}, Name: string(t.Kind)}
	if _, err := mp4.Marshal(w, hdlr, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if err := writeMediaHeader(w, t.Kind); err != nil {
		return err
	}
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
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if err := writeStbl(w, t, tr); err != nil {
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

func writeMediaHeader(w *mp4.Writer, kind model.TrackKind) error {
	if kind == model.KindAudio {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeSmhd()}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.Smhd{}, mp4.Context{}); err != nil {
			return err
		}
		_, err := w.EndBox()
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeVmhd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Vmhd{}, mp4.Context{}); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func writeStbl(w *mp4.Writer, t *TrackSource, tr *TrackResult) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeStsd(w, t); err != nil {
		return err
	}
	// stts: one entry per distinct duration run (usually one).
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	type run struct {
		dur   uint64
		count uint32
	}
	var runs []run
	for _, cf := range tr.Committed {
		if len(runs) > 0 && runs[len(runs)-1].dur == cf.Duration {
			runs[len(runs)-1].count++
		} else {
			runs = append(runs, run{dur: cf.Duration, count: 1})
		}
	}
	stts := &mp4.Stts{EntryCount: uint32(len(runs))}
	for _, r := range runs {
		stts.Entries = append(stts.Entries, mp4.SttsEntry{SampleCount: r.count, SampleDelta: uint32(r.dur)})
	}
	if _, err := mp4.Marshal(w, stts, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// stsc: one sample per chunk keeps offsets unambiguous.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return err
	}
	stsc := &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}}}
	if _, err := mp4.Marshal(w, stsc, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	stsz := &mp4.Stsz{SampleCount: uint32(len(tr.Committed))}
	for _, cf := range tr.Committed {
		stsz.EntrySize = append(stsz.EntrySize, uint32(cf.Size))
	}
	if _, err := mp4.Marshal(w, stsz, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// stco: one chunk per sample.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return err
	}
	stco := &mp4.Stco{EntryCount: uint32(len(tr.Committed))}
	for _, cf := range tr.Committed {
		stco.ChunkOffset = append(stco.ChunkOffset, uint32(cf.Offset))
	}
	if _, err := mp4.Marshal(w, stco, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	// stss only when there are non-sync samples.
	hasNonSync := false
	for _, cf := range tr.Committed {
		if !cf.Sync {
			hasNonSync = true
		}
	}
	if hasNonSync {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStss()}); err != nil {
			return err
		}
		stss := &mp4.Stss{}
		for i, cf := range tr.Committed {
			if cf.Sync {
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
	_, err := w.EndBox()
	return err
}

func writeStsd(w *mp4.Writer, t *TrackSource) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	codec := t.Codec
	if len(codec) != 4 {
		if t.Kind == model.KindAudio {
			codec = "mp4a"
		} else {
			codec = "avc1"
		}
	}
	boxType := mp4.StrToBoxType(codec)
	entry, err := boxType.New(mp4.Context{})
	if err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: boxType}); err != nil {
		return err
	}
	if t.Kind == model.KindAudio {
		v := entry.(*mp4.AudioSampleEntry)
		v.SampleEntry.DataReferenceIndex = 1
		v.ChannelCount = 2
		v.SampleSize = 16
		v.SampleRate = t.Timebase.Timescale << 16
	} else {
		v := entry.(*mp4.VisualSampleEntry)
		v.SampleEntry.DataReferenceIndex = 1
		v.Width, v.Height = 1920, 1080
		v.Horizresolution, v.Vertresolution = 0x00480000, 0x00480000
		v.FrameCount = 1
		v.Depth = 0x0018
	}
	if _, err := mp4.Marshal(w, entry, mp4.Context{}); err != nil {
		return err
	}
	if t.Kind == model.KindVideo {
		if err := writeVisualFields(w, t); err != nil {
			return err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	_, err = w.EndBox()
	return err
}

// writeVisualFields re-attaches the resolved colour description and crop so
// the output describes the essence the way the arbitration decided.
func writeVisualFields(w *mp4.Writer, t *TrackSource) error {
	p, tr, m, full := colourCodes(t.Color)
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeColr()}); err != nil {
		return err
	}
	colr := &mp4.Colr{
		ColourType:              [4]byte{'n', 'c', 'l', 'x'},
		ColourPrimaries:         p,
		TransferCharacteristics: tr,
		MatrixCoefficients:      m,
		FullRangeFlag:           full,
	}
	if _, err := mp4.Marshal(w, colr, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if t.Crop != nil {
		clap := make([]byte, 8+32)
		binary.BigEndian.PutUint32(clap[0:4], uint32(len(clap)))
		copy(clap[4:8], "clap")
		vals := []uint32{
			uint32(t.Crop.Right - t.Crop.Left), 1,
			uint32(t.Crop.Bottom - t.Crop.Top), 1,
			uint32(t.Crop.Left), 1,
			uint32(t.Crop.Top), 1,
		}
		for i, v := range vals {
			binary.BigEndian.PutUint32(clap[8+i*4:], v)
		}
		if _, err := w.Write(clap); err != nil {
			return err
		}
	}
	return nil
}

func colourCodes(c model.ColorDescription) (primaries, transfer, matrix uint16, full bool) {
	primaries, transfer, matrix = 2, 2, 2
	switch c.Primaries.Value {
	case "bt709":
		primaries = 1
	case "bt2020":
		primaries = 9
	}
	switch c.Transfer.Value {
	case "bt709":
		transfer = 1
	case "smpte2084":
		transfer = 16
	}
	switch c.Matrix.Value {
	case "bt709":
		matrix = 1
	case "bt2020nc":
		matrix = 9
	}
	full = c.Range.Value == "full"
	return
}

func rotationMatrix(deg int) [9]int32 {
	one := int32(0x10000)
	switch ((deg % 360) + 360) % 360 {
	case 90:
		return [9]int32{0, one, 0, -one, 0, 0, 0, 0, 0x40000000}
	case 180:
		return [9]int32{-one, 0, 0, 0, -one, 0, 0, 0, 0x40000000}
	case 270:
		return [9]int32{0, -one, 0, one, 0, 0, 0, 0, 0x40000000}
	default:
		return [9]int32{one, 0, 0, 0, one, 0, 0, 0, 0x40000000}
	}
}
