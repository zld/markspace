package remux

import (
	"github.com/abema/go-mp4"

	"mediacore/internal/fault"
)

func writeStbl(w *mp4.Writer, tc trackConfig) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeStsd(w, tc); err != nil {
		return err
	}

	sttsEntries := make([]mp4.SttsEntry, 0, len(tc.items))
	sizes := make([]uint32, 0, len(tc.items))
	offsets := make([]uint32, 0, len(tc.items))
	var previous int64
	for i, ps := range tc.items {
		size := ps.ref.Size
		if ps.ref.Data != nil {
			size = uint32(len(ps.ref.Data))
		}
		sizes = append(sizes, size)
		offsets = append(offsets, ps.offset)
		delta := int64(ps.ref.DurationUnits)
		if i+1 < len(tc.items) {
			delta = tc.items[i+1].ref.DTSUnits - ps.ref.DTSUnits
		}
		if delta <= 0 {
			delta = int64(ps.ref.DurationUnits)
		}
		if delta <= 0 {
			delta = 1
		}
		if i > 0 && ps.ref.DTSUnits < previous {
			return fault.New(fault.Conflict, "output DTS is not monotonic")
		}
		previous = ps.ref.DTSUnits
		sttsEntries = append(sttsEntries, mp4.SttsEntry{SampleCount: 1, SampleDelta: uint32(delta)})
	}

	if err := writeFullBox(w, mp4.BoxTypeStts(), &mp4.Stts{EntryCount: uint32(len(sttsEntries)), Entries: sttsEntries}); err != nil {
		return err
	}
	if err := writeFullBox(w, mp4.BoxTypeStsc(), &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}},
	}); err != nil {
		return err
	}
	if err := writeFullBox(w, mp4.BoxTypeStsz(), &mp4.Stsz{SampleCount: uint32(len(sizes)), EntrySize: sizes}); err != nil {
		return err
	}
	if err := writeFullBox(w, mp4.BoxTypeStco(), &mp4.Stco{EntryCount: uint32(len(offsets)), ChunkOffset: offsets}); err != nil {
		return err
	}
	_, err := w.EndBox() // stbl
	return err
}

func writeStsd(w *mp4.Writer, tc trackConfig) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, &mp4.Stsd{EntryCount: 1}, mp4.Context{}); err != nil {
		return err
	}
	codec := tc.mapping.Codec
	if codec == "" {
		codec = tc.track.Codec
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.StrToBoxType(codec)}); err != nil {
		return err
	}

	var payload mp4.IImmutableBox
	switch codec {
	case "mp4v":
		payload = &mp4.VisualSampleEntry{
			SampleEntry:     mp4.SampleEntry{DataReferenceIndex: 1},
			Width:           tc.track.Width,
			Height:          tc.track.Height,
			Horizresolution: 0x00480000,
			Vertresolution:  0x00480000,
			FrameCount:      1,
			Depth:           0x0018,
			PreDefined3:     -1,
		}
	case "mp4a":
		rate := uint32(48000)
		if tc.track.SampleRate != 0 {
			rate = tc.track.SampleRate
		}
		channels := tc.track.Channels
		if channels == 0 {
			channels = 2
		}
		payload = &mp4.AudioSampleEntry{
			SampleEntry:  mp4.SampleEntry{DataReferenceIndex: 1},
			ChannelCount: channels,
			SampleSize:   16,
			SampleRate:   rate << 16,
		}
	default:
		return fault.New(fault.UnsupportedCodec, "remux writer refuses unknown codec "+codec)
	}
	if err := marshalBox(w, payload); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // codec sample entry
		return err
	}
	_, err := w.EndBox() // stsd
	return err
}

func writeFullBox(w *mp4.Writer, typ mp4.BoxType, payload mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return err
	}
	if err := marshalBox(w, payload); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}
