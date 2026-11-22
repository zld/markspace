package remux

import (
	mp4 "github.com/abema/go-mp4"

	"mediasync/internal/demux"
)

func writeStbl(mw *mp4.Writer, track *demux.Track, samples []Sample) error {
	// Sample table with the structural boxes a reader expects. Payload sizes
	// and offsets are derived from the verbatim samples; no decoding occurs.
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	n := 0
	var sizes []uint32
	for _, s := range samples {
		if s.Track != track.Number {
			continue
		}
		sizes = append(sizes, uint32(len(s.Payload)))
		n++
	}
	// stsd
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	if err := writeSampleEntry(mw, track); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	// stts: one run of identical sample durations.
	if err := writeStts(mw, n, samples, track); err != nil {
		return err
	}
	// stsc: all samples in one chunk.
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(n), SampleDescriptionIndex: 1}},
	}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	// stsz
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Stsz{SampleCount: uint32(n), EntrySize: sizes}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	// stco: placeholder; mdat offset is patched by callers that need seeking.
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // stbl
		return err
	}
	return nil
}

func writeStts(mw *mp4.Writer, n int, samples []Sample, track *demux.Track) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return err
	}
	delta := uint32(0)
	if n > 1 {
		first, last := firstLast(samples, track)
		delta = uint32((last - first) / int64(n-1) / track.TimecodeScaleNS)
	}
	if delta == 0 {
		delta = 1
	}
	_, err := mp4.Marshal(mw, &mp4.Stts{
		EntryCount: 1,
		Entries:    []mp4.SttsEntry{{SampleCount: uint32(n), SampleDelta: delta}},
	}, mp4.Context{})
	if err != nil {
		return err
	}
	_, err = mw.EndBox()
	return err
}

func writeSampleEntry(mw *mp4.Writer, track *demux.Track) error {
	boxType := mp4.BoxTypeOpus()
	if track.Type == demux.TypeVideo {
		boxType = mp4.BoxTypeVp09()
		if track.CodecID == "V_VP8" {
			boxType = mp4.BoxTypeVp08()
		}
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: boxType}); err != nil {
		return err
	}
	// Sample entries are AnyType boxes: their concrete GetType is empty until
	// SetType binds the box type selected above, which the marshaller needs
	// to resolve the registered field layout.
	if track.Type == demux.TypeVideo {
		entry := &mp4.VisualSampleEntry{
			Width:  uint16(track.VideoWidth),
			Height: uint16(track.VideoHeight),
		}
		entry.SetType(boxType)
		if _, err := mp4.Marshal(mw, entry, mp4.Context{}); err != nil {
			return err
		}
	} else {
		entry := &mp4.AudioSampleEntry{
			ChannelCount: uint16(track.AudioChannels),
			SampleRate:   uint32(track.AudioFreq) << 16,
		}
		entry.SetType(boxType)
		if _, err := mp4.Marshal(mw, entry, mp4.Context{}); err != nil {
			return err
		}
	}
	_, err := mw.EndBox()
	return err
}
