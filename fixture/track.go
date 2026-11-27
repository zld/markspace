package fixture

import (
	mp4 "github.com/abema/go-mp4"
)

// writeTrack writes one trak subtree: tkhd, mdia(mdhd, hdlr,
// minf(vmhd|smhd, stbl(stsd, stts, stss, stsc, stsz, stco))).
func writeTrack(w *mp4.Writer, ts TrackSpec, chunkOffset uint64, movieDur uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		FullBox:    mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 7}},
		TrackID:    uint32(ts.ID),
		DurationV0: movieDur,
		Matrix:     matrix,
	}
	if ts.Video {
		tkhd.Width = uint32(ts.Width) << 16
		tkhd.Height = uint32(ts.Height) << 16
	} else {
		tkhd.Volume = 0x100
	}
	if err := writeBox(w, tkhd); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	if err := writeBox(w, &mp4.Mdhd{
		FullBox:    mp4.FullBox{Version: 0},
		Timescale:  ts.Timescale,
		DurationV0: uint32(ts.SampleCount) * ts.SampleDelta,
		Language:   [3]byte{'u', 'n', 'd'},
	}); err != nil {
		return err
	}
	handler := [4]byte{'s', 'o', 'u', 'n'}
	if ts.Video {
		handler = [4]byte{'v', 'i', 'd', 'e'}
	}
	if err := writeBox(w, &mp4.Hdlr{
		FullBox:     mp4.FullBox{Version: 0},
		HandlerType: handler,
		Name:        "fixture",
	}); err != nil {
		return err
	}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if ts.Video {
		if err := writeBox(w, &mp4.Vmhd{FullBox: mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 1}}}); err != nil {
			return err
		}
	} else {
		if err := writeBox(w, &mp4.Smhd{FullBox: mp4.FullBox{Version: 0}}); err != nil {
			return err
		}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeStsd(w, ts); err != nil {
		return err
	}
	if err := writeBox(w, buildStts(ts)); err != nil {
		return err
	}
	if ts.Video && ts.KeyframeEvery > 0 {
		stss := &mp4.Stss{FullBox: mp4.FullBox{Version: 0}}
		for n := 1; n <= ts.SampleCount; n += ts.KeyframeEvery {
			stss.SampleNumber = append(stss.SampleNumber, uint32(n))
		}
		stss.EntryCount = uint32(len(stss.SampleNumber))
		if err := writeBox(w, stss); err != nil {
			return err
		}
	}
	if err := writeBox(w, &mp4.Stsc{
		FullBox:   mp4.FullBox{Version: 0},
		EntryCount: 1,
		Entries: []mp4.StscEntry{
			{FirstChunk: 1, SamplesPerChunk: uint32(ts.SampleCount), SampleDescriptionIndex: 1},
		},
	}); err != nil {
		return err
	}
	if err := writeBox(w, &mp4.Stsz{
		FullBox:     mp4.FullBox{Version: 0},
		SampleSize:  uint32(ts.SampleSize),
		SampleCount: uint32(ts.SampleCount),
	}); err != nil {
		return err
	}
	if err := writeBox(w, &mp4.Stco{
		FullBox:     mp4.FullBox{Version: 0},
		EntryCount:  1,
		ChunkOffset: []uint32{uint32(chunkOffset)},
	}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // stbl
		return err
	}
	if _, err := w.EndBox(); err != nil { // minf
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	_, err := w.EndBox() // trak
	return err
}

// buildStts run-length encodes per-sample deltas, honouring ZeroDeltaAt.
func buildStts(ts TrackSpec) *mp4.Stts {
	stts := &mp4.Stts{FullBox: mp4.FullBox{Version: 0}}
	for i := 0; i < ts.SampleCount; i++ {
		delta := ts.SampleDelta
		if i == ts.ZeroDeltaAt {
			delta = 0
		}
		if n := len(stts.Entries); n > 0 && stts.Entries[n-1].SampleDelta == delta {
			stts.Entries[n-1].SampleCount++
		} else {
			stts.Entries = append(stts.Entries, mp4.SttsEntry{SampleCount: 1, SampleDelta: delta})
		}
	}
	stts.EntryCount = uint32(len(stts.Entries))
	return stts
}

// writeStsd writes the sample description with one avc1/mp4a entry.
func writeStsd(w *mp4.Writer, ts TrackSpec) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{
		FullBox:    mp4.FullBox{Version: 0},
		EntryCount: 1,
	}, mp4.Context{}); err != nil {
		return err
	}
	if ts.Video {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeAvc1()}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.VisualSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()},
				DataReferenceIndex: 1,
			},
			Width:           ts.Width,
			Height:          ts.Height,
			Horizresolution: 0x480000,
			Vertresolution:  0x480000,
			FrameCount:      1,
			Depth:           0x18,
			PreDefined3:     -1,
		}, mp4.Context{}); err != nil {
			return err
		}
		if ts.Color.Present {
			if err := writeBox(w, &mp4.Colr{
				ColourType:              [4]byte{'n', 'c', 'l', 'x'},
				ColourPrimaries:         ts.Color.Primaries,
				TransferCharacteristics: ts.Color.Transfer,
				MatrixCoefficients:      ts.Color.Matrix,
				FullRangeFlag:           ts.Color.FullRange,
			}); err != nil {
				return err
			}
		}
		if _, err := w.EndBox(); err != nil { // avc1
			return err
		}
	} else {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMp4a()}); err != nil {
			return err
		}
		if _, err := mp4.Marshal(w, &mp4.AudioSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()},
				DataReferenceIndex: 1,
			},
			ChannelCount: ts.Channels,
			SampleSize:   16,
			SampleRate:   ts.SampleRate << 16,
		}, mp4.Context{}); err != nil {
			return err
		}
		if _, err := w.EndBox(); err != nil { // mp4a
			return err
		}
	}
	_, err := w.EndBox() // stsd
	return err
}
