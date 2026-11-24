package output

import (
	"fmt"

	mp4 "github.com/abema/go-mp4"

	"mediapipeline/internal/clock"
	mpierr "mediapipeline/internal/errors"
)

// scaledDelta converts a sample duration from timeline ticks to the track
// timescale with exact integer scaling (no floating point, no accumulation).
func scaledDelta(ticks uint32, tickRate, timescale uint32) uint32 {
	return uint32((uint64(ticks)*uint64(timescale) + uint64(tickRate)/2) / uint64(tickRate))
}

// writeMdat emits one mdat containing every sample, tracks in order. It
// returns each track's single chunk offset and the total byte count.
func writeMdat(w *mp4.Writer, audio, video *Track) (map[clock.Kind]uint32, int64, error) {
	bi, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()})
	if err != nil {
		return nil, 0, err
	}
	offsets := map[clock.Kind]uint32{}
	writeTrack := func(t *Track, first bool) error {
		if t == nil {
			return nil
		}
		if first {
			offsets[t.Kind] = uint32(bi.Offset + 8) // mdat header is 8 bytes
		} else {
			pos, err := w.Seek(0, 1) // current position = next chunk start
			if err != nil {
				return err
			}
			offsets[t.Kind] = uint32(pos)
		}
		for i := range t.Samples {
			if _, err := w.Write(t.Samples[i].Data); err != nil {
				return err
			}
		}
		return nil
	}
	if err := writeTrack(audio, true); err != nil {
		return nil, 0, err
	}
	if err := writeTrack(video, false); err != nil {
		return nil, 0, err
	}
	end, err := w.EndBox()
	if err != nil {
		return nil, 0, err
	}
	return offsets, int64(end.Offset + end.Size), nil
}

// writeMoov emits the full movie box with one trak per non-nil track. It
// returns the absolute end position of the moov box (the file's final size).
func writeMoov(w *mp4.Writer, audio, video *Track, off map[clock.Kind]uint32) (int64, error) {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return 0, err
	}
	movieDur := trackDuration(audio)
	if video != nil && trackDuration(video) > movieDur {
		movieDur = trackDuration(video)
	}
	mvhd := &mp4.Mvhd{
		Timescale: audio.Timescale, DurationV0: scaledDelta(
			uint32(movieDur), audio.TickRate, audio.Timescale),
		Rate: 0x00010000, Volume: 0x0100, Matrix: identity, NextTrackID: 3,
	}
	if err := box(w, mp4.BoxTypeMvhd(), mvhd); err != nil {
		return 0, err
	}
	trackID := uint32(1)
	if err := writeTrak(w, audio, trackID, off[audio.Kind]); err != nil {
		return 0, err
	}
	if video != nil {
		trackID++
		if err := writeTrak(w, video, trackID, off[video.Kind]); err != nil {
			return 0, err
		}
	}
	end, err := w.EndBox()
	if err != nil {
		return 0, err
	}
	return int64(end.Offset + end.Size), nil
}

// trackDuration sums timeline ticks without modifying any committed state.
func trackDuration(t *Track) uint64 {
	var d uint64
	for i := range t.Samples {
		d += uint64(t.Samples[i].Ticks)
	}
	return d
}

func writeTrak(w *mp4.Writer, t *Track, id uint32, chunkOffset uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	durTicks := trackDuration(t)
	tkhd := &mp4.Tkhd{
		TrackID: id, DurationV0: scaledDelta(uint32(durTicks), t.TickRate, t.Timescale),
		Matrix: identity,
	}
	if t.Kind == clock.KindAudio {
		tkhd.Volume = 0x0100
	} else {
		tkhd.Width, tkhd.Height = 320<<16, 240<<16 // 16.16 fixed point
	}
	if err := box(w, mp4.BoxTypeTkhd(), tkhd); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{Timescale: t.Timescale,
		DurationV0: scaledDelta(uint32(durTicks), t.TickRate, t.Timescale)}
	if err := box(w, mp4.BoxTypeMdhd(), mdhd); err != nil {
		return err
	}
	handler := [4]byte{'v', 'i', 'd', 'e'}
	if t.Kind == clock.KindAudio {
		handler = [4]byte{'s', 'o', 'u', 'n'}
	}
	hdlr := &mp4.Hdlr{HandlerType: handler, Name: string(handler[:])}
	if err := box(w, mp4.BoxTypeHdlr(), hdlr); err != nil {
		return err
	}
	if err := writeMinf(w, t, chunkOffset); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return err
	}
	_, err := w.EndBox() // trak
	return err
}

func writeMinf(w *mp4.Writer, t *Track, chunkOffset uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if t.Kind == clock.KindAudio {
		if err := box(w, mp4.BoxTypeSmhd(), &mp4.Smhd{}); err != nil {
			return err
		}
	} else {
		if err := box(w, mp4.BoxTypeVmhd(), &mp4.Vmhd{}); err != nil {
			return err
		}
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	// dref is a container: marshal its header struct, then the child url box.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	if err := marshalOnly(w, &mp4.Dref{EntryCount: 1}); err != nil {
		return err
	}
	// Self-contained data reference: a "url " FullBox with flag 1 means the
	// media data resides in this same file. StartBox already wrote the box
	// header, so only the 4-byte version+flags payload is needed.
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); err != nil {
		return err
	}
	if _, err := w.Write([]byte{0, 0, 0, 1}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // dref
		return err
	}
	if _, err := w.EndBox(); err != nil { // dinf
		return err
	}
	if err := writeStbl(w, t, chunkOffset); err != nil {
		return err
	}
	_, err := w.EndBox() // minf
	return err
}

func writeStbl(w *mp4.Writer, t *Track, chunkOffset uint32) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeStsd(w, t); err != nil {
		return err
	}
	sizes := make([]uint32, len(t.Samples))
	stts := make([]mp4.SttsEntry, len(t.Samples))
	for i := range t.Samples {
		sizes[i] = uint32(len(t.Samples[i].Data))
		stts[i] = mp4.SttsEntry{SampleCount: 1,
			SampleDelta: scaledDelta(t.Samples[i].Ticks, t.TickRate, t.Timescale)}
	}
	if err := box(w, mp4.BoxTypeStts(), &mp4.Stts{EntryCount: uint32(len(stts)), Entries: stts}); err != nil {
		return err
	}
	if err := box(w, mp4.BoxTypeStsc(), &mp4.Stsc{EntryCount: 1,
		Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(t.Samples)), SampleDescriptionIndex: 1}}}); err != nil {
		return err
	}
	if err := box(w, mp4.BoxTypeStsz(), &mp4.Stsz{SampleCount: uint32(len(sizes)), EntrySize: sizes}); err != nil {
		return err
	}
	if err := box(w, mp4.BoxTypeStco(), &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{chunkOffset}}); err != nil {
		return err
	}
	_, err := w.EndBox() // stbl
	return err
}

func writeStsd(w *mp4.Writer, t *Track) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	stsd := &mp4.Stsd{EntryCount: 1}
	if err := marshalOnly(w, stsd); err != nil {
		return err
	}
	if t.Kind == clock.KindAudio {
		entry := &mp4.AudioSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()},
				DataReferenceIndex: 1,
			},
			ChannelCount: 2, SampleSize: 16,
			SampleRate: t.Timescale << 16, // 16.16 fixed point
		}
		if err := box(w, mp4.BoxTypeMp4a(), entry); err != nil {
			return err
		}
	} else {
		entry := &mp4.VisualSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()},
				DataReferenceIndex: 1,
			},
			Width: 320, Height: 240,
			Horizresolution: 0x00480000, Vertresolution: 0x00480000,
			FrameCount: 1, Depth: 0x0018, PreDefined3: -1,
		}
		if err := box(w, mp4.BoxTypeAvc1(), entry); err != nil {
			return err
		}
	}
	_, err := w.EndBox() // stsd
	return err
}

// box starts a container, marshals the payload struct and closes the box.
func box(w *mp4.Writer, typ mp4.BoxType, payload mp4.IImmutableBox) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: typ}); err != nil {
		return mpierr.New(mpierr.CatUnknown, 0, "mux", fmt.Sprintf("start %s", typ)).Wrap(err)
	}
	if err := marshalOnly(w, payload); err != nil {
		return err
	}
	_, err := w.EndBox()
	return err
}

func marshalOnly(w *mp4.Writer, payload mp4.IImmutableBox) error {
	_, err := mp4.Marshal(w, payload, mp4.Context{})
	if err != nil {
		err = mpierr.New(mpierr.CatUnknown, 0, "mux", "marshal box").Wrap(err)
	}
	return err
}
