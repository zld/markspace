package remux

import (
	mp4 "github.com/abema/go-mp4"
)

// writeTrak emits one trak subtree: tkhd, mdia (mdhd, hdlr, minf with
// the full sample tables that make up the random-access index).
func (w *Writer) writeTrak(t *Track) error {
	if _, err := w.w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		TrackID:    t.ID,
		DurationV0: uint32(t.duration / 1_000_000),
		Matrix:     [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
	}
	tkhd.Flags = [3]byte{0, 0, 3} // enabled | in movie
	if t.Kind == "video" {
		tkhd.Width, tkhd.Height = 640<<16, 360<<16
	} else {
		tkhd.Volume = 0x0100
	}
	if err := w.box(mp4.BoxTypeTkhd(), tkhd); err != nil {
		return err
	}
	if _, err := w.w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := w.box(mp4.BoxTypeMdhd(), &mp4.Mdhd{
		Timescale:  mediaTimescale,
		DurationV0: uint32(t.duration),
		Language:   [3]byte{'u', 'n', 'd'},
	}); err != nil {
		return err
	}
	handler := [4]byte{'s', 'o', 'u', 'n'}
	name := "mediaqc audio"
	if t.Kind == "video" {
		handler = [4]byte{'v', 'i', 'd', 'e'}
		name = "mediaqc video"
	}
	if err := w.box(mp4.BoxTypeHdlr(), &mp4.Hdlr{HandlerType: handler, Name: name}); err != nil {
		return err
	}
	if err := w.writeMinf(t); err != nil {
		return err
	}
	if _, err := w.w.EndBox(); err != nil { // mdia
		return err
	}
	_, err := w.w.EndBox() // trak
	return err
}

func (w *Writer) writeMinf(t *Track) error {
	if _, err := w.w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if t.Kind == "video" {
		vmhd := &mp4.Vmhd{}
		vmhd.Flags = [3]byte{0, 0, 1}
		if err := w.box(mp4.BoxTypeVmhd(), vmhd); err != nil {
			return err
		}
	} else if err := w.box(mp4.BoxTypeSmhd(), &mp4.Smhd{}); err != nil {
		return err
	}
	// dinf > dref > url (self-contained)
	if _, err := w.w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w.w, &mp4.Dref{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	url := &mp4.Url{}
	url.Flags = [3]byte{0, 0, 1}
	if err := w.box(mp4.BoxTypeUrl(), url); err != nil {
		return err
	}
	if _, err := w.w.EndBox(); err != nil { // dref
		return err
	}
	if _, err := w.w.EndBox(); err != nil { // dinf
		return err
	}
	if err := w.writeStbl(t); err != nil {
		return err
	}
	_, err := w.w.EndBox() // minf
	return err
}

// writeStbl emits the sample table: stsd, stts, stsc, stsz, stco and
// the stss sync table that provides random access.
func (w *Writer) writeStbl(t *Track) error {
	if _, err := w.w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	// stsd with one sample entry child.
	if _, err := w.w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w.w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if t.Kind == "video" {
		err := w.box(mp4.BoxTypeMp4v(), &mp4.VisualSampleEntry{
			SampleEntry:     mp4.SampleEntry{DataReferenceIndex: 1},
			Width:           640,
			Height:          360,
			Horizresolution: 0x00480000,
			Vertresolution:  0x00480000,
			FrameCount:      1,
			Depth:           0x18,
			PreDefined3:     -1,
		})
		if err != nil {
			return err
		}
	} else {
		err := w.box(mp4.BoxTypeMp4a(), &mp4.AudioSampleEntry{
			SampleEntry:  mp4.SampleEntry{DataReferenceIndex: 1},
			ChannelCount: 2,
			SampleSize:   16,
			SampleRate:   48000 << 16,
		})
		if err != nil {
			return err
		}
	}
	if _, err := w.w.EndBox(); err != nil { // stsd
		return err
	}
	if err := w.box(mp4.BoxTypeStts(), &mp4.Stts{
		EntryCount: uint32(len(t.sttsEntries())),
		Entries:    t.sttsEntries(),
	}); err != nil {
		return err
	}
	if err := w.box(mp4.BoxTypeStsc(), &mp4.Stsc{
		EntryCount: 1,
		Entries: []mp4.StscEntry{{
			FirstChunk:             1,
			SamplesPerChunk:        1,
			SampleDescriptionIndex: 1,
		}},
	}); err != nil {
		return err
	}
	if err := w.box(mp4.BoxTypeStsz(), &mp4.Stsz{
		SampleSize:  0,
		SampleCount: uint32(len(t.sizes)),
		EntrySize:   t.sizes,
	}); err != nil {
		return err
	}
	chunks := make([]uint32, len(t.chunks))
	for i, c := range t.chunks {
		chunks[i] = uint32(c)
	}
	if err := w.box(mp4.BoxTypeStco(), &mp4.Stco{
		EntryCount:  uint32(len(chunks)),
		ChunkOffset: chunks,
	}); err != nil {
		return err
	}
	if len(t.keyframes) > 0 {
		if err := w.box(mp4.BoxTypeStss(), &mp4.Stss{
			EntryCount:   uint32(len(t.keyframes)),
			SampleNumber: t.keyframes,
		}); err != nil {
			return err
		}
	}
	_, err := w.w.EndBox() // stbl
	return err
}

// sttsEntries run-length encodes the per-sample durations.
func (t *Track) sttsEntries() []mp4.SttsEntry {
	var out []mp4.SttsEntry
	for _, d := range t.deltas {
		if n := len(out); n > 0 && out[n-1].SampleDelta == d {
			out[n-1].SampleCount++
			continue
		}
		out = append(out, mp4.SttsEntry{SampleCount: 1, SampleDelta: d})
	}
	return out
}
