package mediacore

import (
	mp4 "github.com/abema/go-mp4"
)

// muxSample is the container-side view of a sample used when writing a
// sample table. It carries no payload.
type muxSample struct {
	Size int64
	DTS  int64
	Dur  uint32
	CTS  int64
	Sync bool
}

// muxTrack describes a track to be written into a moov box. RawEntry, when
// set, is copied verbatim as the stsd sample entry (used to preserve tracks
// whose codec structure this package does not rebuild).
type muxTrack struct {
	ID        uint32
	Kind      TrackKind
	Handler   string
	Timescale uint32
	Width     uint16
	Height    uint16
	Channels  uint16
	RawEntry  []byte
	Samples   []muxSample
}

type muxChunk struct {
	Track int
	First int
	Count int
	Start int64
}

// planMuxLayout assigns samples to chunks (two per chunk, with a trailing
// single sample folded into the previous chunk) and returns the absolute
// start offset of every chunk plus the end of the payload.
func planMuxLayout(tracks []muxTrack, base int64) ([]muxChunk, int64) {
	var chunks []muxChunk
	for ti, tr := range tracks {
		var groups [][2]int // {first, count}
		n := len(tr.Samples)
		for first := 0; first < n; first += 2 {
			count := 2
			if first+count > n {
				count = n - first
			}
			if count == 1 && len(groups) > 0 {
				prev := &groups[len(groups)-1]
				if prev[1] < 3 {
					prev[1]++
					continue
				}
			}
			groups = append(groups, [2]int{first, count})
		}
		for _, g := range groups {
			var size int64
			for i := 0; i < g[1]; i++ {
				size += tr.Samples[g[0]+i].Size
			}
			chunks = append(chunks, muxChunk{Track: ti, First: g[0], Count: g[1], Start: base})
			base += size
		}
	}
	return chunks, base
}

// buildMoov assembles a complete moov box in memory. The moov only contains
// index data, so holding it in memory is allowed; payload streams separately.
func buildMoov(tracks []muxTrack, chunks []muxChunk, movieTimescale uint32, movieDuration int64) ([]byte, error) {
	ms := &memSeeker{}
	w := mp4.NewWriter(ms)
	ctx := mp4.Context{}

	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMoov()}); err != nil {
		return nil, wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMvhd()}); err != nil {
		return nil, wrapErr(KindInternal, "mux", err)
	}
	mvhd := &mp4.Mvhd{
		Timescale:   movieTimescale,
		DurationV0:  uint32(movieDuration),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix(),
		NextTrackID: nextTrackID(tracks),
	}
	if _, err := mp4.Marshal(w, mvhd, ctx); err != nil {
		return nil, wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return nil, wrapErr(KindInternal, "mux", err)
	}

	for ti := range tracks {
		if err := writeTrak(w, ctx, tracks, chunks, ti); err != nil {
			return nil, err
		}
	}
	if _, err := w.EndBox(); err != nil {
		return nil, wrapErr(KindInternal, "mux", err)
	}
	return ms.Bytes(), nil
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func nextTrackID(tracks []muxTrack) uint32 {
	var max uint32
	for _, t := range tracks {
		if t.ID > max {
			max = t.ID
		}
	}
	return max + 1
}

func writeTrak(w *mp4.Writer, ctx mp4.Context, tracks []muxTrack, allChunks []muxChunk, ti int) error {
	tr := tracks[ti]
	chunks := chunkSlice(allChunks, ti)
	durMovie, err := Rescale(trackEnd(tr), tr.Timescale, 1000)
	if err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTkhd()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	tkhd := &mp4.Tkhd{
		TrackID:    tr.ID,
		DurationV0: uint32(durMovie),
		Matrix:     identityMatrix(),
	}
	switch tr.Kind {
	case KindVideoTrack:
		tkhd.Width = uint32(tr.Width) << 16
		tkhd.Height = uint32(tr.Height) << 16
	case KindAudioTrack:
		tkhd.Volume = 0x0100
	}
	if _, err := mp4.Marshal(w, tkhd, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdhd()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Mdhd{Timescale: tr.Timescale, DurationV0: uint32(trackEnd(tr)), Language: [3]byte{'u', 'n', 'd'}}, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if err := writeHdlr(w, ctx, tr); err != nil {
		return err
	}
	if err := writeMinf(w, ctx, tracks, chunks, ti); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // mdia
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil { // trak
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

func trackEnd(tr muxTrack) int64 {
	if len(tr.Samples) == 0 {
		return 0
	}
	last := tr.Samples[len(tr.Samples)-1]
	return last.DTS + int64(last.Dur)
}

func writeHdlr(w *mp4.Writer, ctx mp4.Context, tr muxTrack) error {
	handler := [4]byte{'v', 'i', 'd', 'e'}
	switch tr.Handler {
	case "soun":
		handler = [4]byte{'s', 'o', 'u', 'n'}
	case "meta":
		handler = [4]byte{'m', 'e', 't', 'a'}
	}
	name := "VideoHandler"
	switch handler {
	case [4]byte{'s', 'o', 'u', 'n'}:
		name = "SoundHandler"
	case [4]byte{'m', 'e', 't', 'a'}:
		name = "MetadataHandler"
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeHdlr()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Hdlr{HandlerType: handler, Name: name}, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

func writeMinf(w *mp4.Writer, ctx mp4.Context, tracks []muxTrack, chunks []muxChunk, ti int) error {
	tr := tracks[ti]
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if err := writeMediaHeader(w, ctx, tr); err != nil {
		return err
	}
	if err := writeDinf(w, ctx); err != nil {
		return err
	}
	if err := writeStbl(w, ctx, tracks, chunks, ti); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

func writeMediaHeader(w *mp4.Writer, ctx mp4.Context, tr muxTrack) error {
	if tr.Kind == KindAudioTrack {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeSmhd()}); err != nil {
			return wrapErr(KindInternal, "mux", err)
		}
		if _, err := mp4.Marshal(w, &mp4.Smhd{}, ctx); err != nil {
			return wrapErr(KindInternal, "mux", err)
		}
		if _, err := w.EndBox(); err != nil {
			return wrapErr(KindInternal, "mux", err)
		}
		return nil
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeVmhd()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Vmhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

func writeDinf(w *mp4.Writer, ctx mp4.Context) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Dref{EntryCount: 1}, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeUrl()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := mp4.Marshal(w, &mp4.Url{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

// writeSampleEntry writes the stsd entry, either verbatim or rebuilt.
func writeSampleEntry(w *mp4.Writer, ctx mp4.Context, tr muxTrack) error {
	if len(tr.RawEntry) > 0 {
		if _, err := w.Write(tr.RawEntry); err != nil {
			return wrapErr(KindInternal, "mux", err)
		}
		return nil
	}
	if tr.Kind == KindAudioTrack {
		if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMp4a()}); err != nil {
			return wrapErr(KindInternal, "mux", err)
		}
		entry := &mp4.AudioSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()},
				DataReferenceIndex: 1,
			},
			ChannelCount: tr.Channels,
			SampleSize:   16,
			SampleRate:   tr.Timescale << 16,
		}
		if _, err := mp4.Marshal(w, entry, ctx); err != nil {
			return wrapErr(KindInternal, "mux", err)
		}
		if _, err := w.EndBox(); err != nil {
			return wrapErr(KindInternal, "mux", err)
		}
		return nil
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeAvc1()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	entry := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()},
			DataReferenceIndex: 1,
		},
		Width:           tr.Width,
		Height:          tr.Height,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           24,
		PreDefined3:     -1,
	}
	if _, err := mp4.Marshal(w, entry, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if err := writeAvcC(w, ctx); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

func writeAvcC(w *mp4.Writer, ctx mp4.Context) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeAvcC()}); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	avcc := &mp4.AVCDecoderConfiguration{
		AnyTypeBox:                 mp4.AnyTypeBox{Type: mp4.BoxTypeAvcC()},
		ConfigurationVersion:       1,
		Profile:                    0x42,
		Level:                      0x1e,
		LengthSizeMinusOne:         3,
		NumOfSequenceParameterSets: 1,
		SequenceParameterSets:      []mp4.AVCParameterSet{{Length: 4, NALUnit: []byte{0x67, 0x42, 0x00, 0x1e}}},
		NumOfPictureParameterSets:  1,
		PictureParameterSets:       []mp4.AVCParameterSet{{Length: 2, NALUnit: []byte{0x68, 0xce}}},
	}
	if _, err := mp4.Marshal(w, avcc, ctx); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	if _, err := w.EndBox(); err != nil {
		return wrapErr(KindInternal, "mux", err)
	}
	return nil
}

// chunkSlice filters the global chunk plan to one track.
func chunkSlice(chunks []muxChunk, trackIndex int) []muxChunk {
	var out []muxChunk
	for _, ch := range chunks {
		if ch.Track == trackIndex {
			out = append(out, ch)
		}
	}
	return out
}
