// Package media: ISO-BMFF box construction.
package media

import (
	"github.com/abema/go-mp4"

	"example.com/assetpack/internal/audio"
)

func writeMvhd(w *mp4.Writer, ctx mp4.Context, duration uint64, rate int, creation uint32) error {
	if err := start(w, mp4.BoxTypeMvhd()); err != nil {
		return err
	}
	mvhd := &mp4.Mvhd{
		Timescale: 1000, DurationV0: uint32(duration * 1000 / uint64(rate)),
		Rate: 0x00010000, Volume: 0x0100,
		Matrix:      [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
		NextTrackID: 2,
	}
	mvhd.CreationTimeV0 = creation
	mvhd.ModificationTimeV0 = creation
	if _, err := mp4.Marshal(w, mvhd, ctx); err != nil {
		return wrap("mvhd", err)
	}
	_, err := w.EndBox()
	return err
}

func writeTkhd(w *mp4.Writer, ctx mp4.Context, duration uint64, creation uint32) error {
	if err := start(w, mp4.BoxTypeTkhd()); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		TrackID: 1, DurationV0: uint32(duration), Volume: 0x0100,
		Matrix: [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000},
	}
	tkhd.CreationTimeV0 = creation
	tkhd.ModificationTimeV0 = creation
	if _, err := mp4.Marshal(w, tkhd, ctx); err != nil {
		return wrap("tkhd", err)
	}
	_, err := w.EndBox()
	return err
}

func writeMdhd(w *mp4.Writer, ctx mp4.Context, duration uint64, rate int, creation uint32) error {
	if err := start(w, mp4.BoxTypeMdhd()); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{
		Timescale: uint32(rate), DurationV0: uint32(duration),
		Language: [3]byte{'u', 'n', 'd'},
	}
	mdhd.CreationTimeV0 = creation
	mdhd.ModificationTimeV0 = creation
	if _, err := mp4.Marshal(w, mdhd, ctx); err != nil {
		return wrap("mdhd", err)
	}
	_, err := w.EndBox()
	return err
}

func writeHdlr(w *mp4.Writer, ctx mp4.Context) error {
	if err := start(w, mp4.BoxTypeHdlr()); err != nil {
		return err
	}
	hdlr := &mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "SoundHandler"}
	if _, err := mp4.Marshal(w, hdlr, ctx); err != nil {
		return wrap("hdlr", err)
	}
	_, err := w.EndBox()
	return err
}

func writeSmhd(w *mp4.Writer, ctx mp4.Context) error {
	if err := start(w, mp4.BoxTypeSmhd()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Smhd{}, ctx); err != nil {
		return wrap("smhd", err)
	}
	_, err := w.EndBox()
	return err
}

func writeDinf(w *mp4.Writer, ctx mp4.Context) error {
	if err := start(w, mp4.BoxTypeDinf()); err != nil {
		return err
	}
	if err := start(w, mp4.BoxTypeDref()); err != nil {
		return err
	}
	dref := &mp4.Dref{EntryCount: 1}
	if _, err := mp4.Marshal(w, dref, ctx); err != nil {
		return wrap("dref", err)
	}
	url := &mp4.Url{}
	url.SetFlags(mp4.UrlSelfContained)
	if err := start(w, mp4.BoxTypeUrl()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, url, ctx); err != nil {
		return wrap("url", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrap("url", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrap("dref", err)
	}
	_, err := w.EndBox()
	return err
}

func writeStbl(w *mp4.Writer, ctx mp4.Context, layout layoutInfo, p audio.PCM) error {
	// stsd: one ipcm entry carrying pcmC.
	if err := start(w, mp4.BoxTypeStsd()); err != nil {
		return err
	}
	stsd := &mp4.Stsd{EntryCount: 1}
	if _, err := mp4.Marshal(w, stsd, ctx); err != nil {
		return wrap("stsd", err)
	}
	entryType := mp4.StrToBoxType("ipcm")
	ase := &mp4.AudioSampleEntry{
		ChannelCount: uint16(len(p.Channels)),
		SampleSize:   bitsPerSample,
		SampleRate:   uint32(p.Format.Rate) << 16,
	}
	ase.SetType(entryType)
	if err := start(w, entryType); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, ase, ctx); err != nil {
		return wrap("ipcm", err)
	}
	pcmC := &mp4.PcmC{FormatFlags: 0, PCMSampleSize: bitsPerSample}
	if err := start(w, mp4.BoxTypePcmC()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, pcmC, ctx); err != nil {
		return wrap("pcmC", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrap("pcmC", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrap("ipcm", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrap("stsd", err)
	}

	// stts: full-length samples plus a possibly short final sample.
	if err := start(w, mp4.BoxTypeStts()); err != nil {
		return err
	}
	var runs []mp4.SttsEntry
	if layout.count > 1 {
		runs = append(runs, mp4.SttsEntry{SampleCount: uint32(layout.count - 1), SampleDelta: ChunkFrames})
	}
	runs = append(runs, mp4.SttsEntry{SampleCount: 1, SampleDelta: uint32(layout.last)})
	stts := &mp4.Stts{Entries: runs}
	stts.EntryCount = uint32(len(runs))
	if _, err := mp4.Marshal(w, stts, ctx); err != nil {
		return wrap("stts", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrap("stts", err)
	}

	// stsc: one chunk holding every sample.
	if err := start(w, mp4.BoxTypeStsc()); err != nil {
		return err
	}
	stsc := &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{
		FirstChunk: 1, SamplesPerChunk: uint32(layout.count), SampleDescriptionIndex: 1,
	}}}
	if _, err := mp4.Marshal(w, stsc, ctx); err != nil {
		return wrap("stsc", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrap("stsc", err)
	}

	// stsz: per-sample byte sizes.
	if err := start(w, mp4.BoxTypeStsz()); err != nil {
		return err
	}
	stsz := &mp4.Stsz{SampleCount: uint32(layout.count)}
	for _, s := range layout.sizes {
		stsz.EntrySize = append(stsz.EntrySize, uint32(s))
	}
	if _, err := mp4.Marshal(w, stsz, ctx); err != nil {
		return wrap("stsz", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrap("stsz", err)
	}

	// stco: the mdat payload starts right after the moov box, so the offset
	// is patched once the moov size is known.  Record a placeholder now.
	if err := start(w, mp4.BoxTypeStco()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}}, ctx); err != nil {
		return wrap("stco", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrap("stco", err)
	}
	return nil
}
