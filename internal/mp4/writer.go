package mp4mux

import (
	"github.com/abema/go-mp4"

	"mediarun/internal/fault"
)

// writeVideoTrak emits the video track with an AVC sample entry. The fixture
// frames are opaque to the muxer; the avcC record here is a minimal valid
// configuration so players and parsers accept the file.
func writeVideoTrak(w *mp4.Writer, samples []Sample, dur uint64, cfg Config, chunkStart int64) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		TrackID:    1,
		DurationV0: uint32(dur),
		Matrix:     identityMatrix(),
		Width:      uint32(cfg.Width) << 16,
		Height:     uint32(cfg.Height) << 16,
	}
	tkhd.SetFlags(0x000007)
	if err := writeBox(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{Timescale: cfg.Timescale, DurationV0: uint32(dur), Language: [3]byte{'u', 'n', 'd'}}
	if err := writeBox(w, mdhd, mp4.Context{}); err != nil {
		return err
	}
	if err := writeBox(w, &mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "VideoHandler"}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	vmhd := &mp4.Vmhd{}
	vmhd.SetFlags(1)
	if err := writeBox(w, vmhd, mp4.Context{}); err != nil {
		return err
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeVideoStsd(w, cfg); err != nil {
		return err
	}
	if err := writeStts(w, videoDurations(samples)); err != nil {
		return err
	}
	stss := &mp4.Stss{}
	for i, s := range samples {
		if s.Keyframe {
			stss.SampleNumber = append(stss.SampleNumber, uint32(i+1))
		}
	}
	if len(stss.SampleNumber) > 0 {
		stss.EntryCount = uint32(len(stss.SampleNumber))
		if err := writeBox(w, stss, mp4.Context{}); err != nil {
			return err
		}
	}
	stsc := &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{
		FirstChunk: 1, SamplesPerChunk: uint32(len(samples)), SampleDescriptionIndex: 1,
	}}}
	if err := writeBox(w, stsc, mp4.Context{}); err != nil {
		return err
	}
	stsz := &mp4.Stsz{SampleCount: uint32(len(samples))}
	for _, s := range samples {
		stsz.EntrySize = append(stsz.EntrySize, uint32(len(s.Record.Payload)))
	}
	if err := writeBox(w, stsz, mp4.Context{}); err != nil {
		return err
	}
	if err := writeChunkOffset(w, chunkStart); err != nil {
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

// writeAudioTrak emits the audio track as Opus. Concealment slots inflate the
// stts duration without adding samples, matching the unit timeline.
func writeAudioTrak(w *mp4.Writer, audio []AudioSample, dur uint64, cfg Config, chunkStart int64) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeTrak()}); err != nil {
		return err
	}
	tkhd := &mp4.Tkhd{
		TrackID:    2,
		DurationV0: uint32(dur),
		Matrix:     identityMatrix(),
		Volume:     0x0100,
	}
	tkhd.SetFlags(0x000007)
	if err := writeBox(w, tkhd, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdia()}); err != nil {
		return err
	}
	mdhd := &mp4.Mdhd{Timescale: cfg.AudioTimescale, DurationV0: uint32(dur), Language: [3]byte{'u', 'n', 'd'}}
	if err := writeBox(w, mdhd, mp4.Context{}); err != nil {
		return err
	}
	if err := writeBox(w, &mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "SoundHandler"}, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return err
	}
	if err := writeBox(w, &mp4.Smhd{}, mp4.Context{}); err != nil {
		return err
	}
	if err := writeDinf(w); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return err
	}
	if err := writeAudioStsd(w, cfg.AudioTimescale); err != nil {
		return err
	}
	// A concealment slot carries no bytes. Instead of emitting a zero-length
	// sample (which confuses strict parsers and breaks stts/stsz agreement),
	// its duration is folded into the preceding real sample; a leading run of
	// concealment is folded forward. stts sample count then always equals stsz
	// sample count, and the track duration still matches the unit timeline.
	durs, sizes := audioSamples(audio)
	if len(sizes) == 0 {
		// Keep stsc/stsz legal for a fully concealed track.
		sizes = []uint32{0}
		durs = []uint32{uint32(dur)}
	}
	if err := writeStts(w, durs); err != nil {
		return err
	}
	stsc := &mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{
		FirstChunk: 1, SamplesPerChunk: uint32(len(sizes)), SampleDescriptionIndex: 1,
	}}}
	if err := writeBox(w, stsc, mp4.Context{}); err != nil {
		return err
	}
	stsz := &mp4.Stsz{SampleCount: uint32(len(sizes)), EntrySize: sizes}
	if err := writeBox(w, stsz, mp4.Context{}); err != nil {
		return err
	}
	if err := writeChunkOffset(w, chunkStart); err != nil {
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

// audioSamples collapses concealment slots into neighbouring real samples.
// It returns one duration per emitted sample and the matching sizes; the sum of
// durations equals the full unit timeline.
func audioSamples(audio []AudioSample) ([]uint32, []uint32) {
	var durs, sizes []uint32
	var pending uint64
	for _, a := range audio {
		if len(a.Data) == 0 {
			pending += uint64(a.Duration)
			continue
		}
		d := uint64(a.Duration) + pending
		pending = 0
		if d > uint64(^uint32(0)) {
			d = uint64(^uint32(0))
		}
		durs = append(durs, uint32(d))
		sizes = append(sizes, uint32(len(a.Data)))
	}
	if pending > 0 {
		if len(durs) > 0 {
			d := uint64(durs[len(durs)-1]) + pending
			if d > uint64(^uint32(0)) {
				d = uint64(^uint32(0))
			}
			durs[len(durs)-1] = uint32(d)
		}
		// With no real sample at all there is nothing to hang the duration on;
		// the caller substitutes a single duration-only sample.
	}
	return durs, sizes
}

func videoDurations(samples []Sample) []uint32 {
	out := make([]uint32, len(samples))
	for i, s := range samples {
		out[i] = s.Duration
	}
	return out
}

// writeStts compresses equal consecutive durations into single entries.
func writeStts(w *mp4.Writer, durs []uint32) error {
	stts := &mp4.Stts{}
	for i := 0; i < len(durs); {
		j := i
		for j < len(durs) && durs[j] == durs[i] {
			j++
		}
		stts.Entries = append(stts.Entries, mp4.SttsEntry{
			SampleCount: uint32(j - i),
			SampleDelta: durs[i],
		})
		i = j
	}
	stts.EntryCount = uint32(len(stts.Entries))
	return writeBox(w, stts, mp4.Context{})
}

func writeChunkOffset(w *mp4.Writer, off int64) error {
	if off < 0 || off > int64(^uint32(0)) {
		return fault.New(fault.StageMux, fault.KindLimit, "chunk offset %d does not fit stco", off)
	}
	return writeBox(w, &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{uint32(off)}}, mp4.Context{})
}

// writeDinf emits a self-contained data reference.
//
// dref is a container that starts with a FullBox + entry count payload, so its
// header is opened, its own fields are marshalled, the url child is written and
// only then is the dref size patched.
func writeDinf(w *mp4.Writer) error {
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDinf()}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeDref()}); err != nil {
		return err
	}
	dref := &mp4.Dref{EntryCount: 1}
	if _, err := mp4.Marshal(w, dref, mp4.Context{}); err != nil {
		return err
	}
	url := &mp4.Url{}
	url.SetFlags(mp4.UrlSelfContained)
	if err := writeBox(w, url, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // dref
		return err
	}
	_, err := w.EndBox() // dinf
	return err
}

// writeVideoStsd emits an avc1 sample entry with a minimal avcC.
func writeVideoStsd(w *mp4.Writer, cfg Config) error {
	stsd := &mp4.Stsd{EntryCount: 1}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, stsd, mp4.Context{}); err != nil {
		return err
	}
	vse := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()},
			DataReferenceIndex: 1,
		},
		Width:           uint16(cfg.Width),
		Height:          uint16(cfg.Height),
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeAvc1()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, vse, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeAvcC()}); err != nil {
		return err
	}
	avcc := &mp4.AVCDecoderConfiguration{
		AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeAvcC()},
		Profile:            0x64,
		Level:              0x1F,
		LengthSizeMinusOne: 3,
	}
	if _, err := mp4.Marshal(w, avcc, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // avcC
		return err
	}
	if _, err := w.EndBox(); err != nil { // avc1
		return err
	}
	_, err := w.EndBox() // stsd
	return err
}

// writeAudioStsd emits an Opus sample entry with its dOps configuration.
func writeAudioStsd(w *mp4.Writer, rate uint32) error {
	stsd := &mp4.Stsd{EntryCount: 1}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, stsd, mp4.Context{}); err != nil {
		return err
	}
	ase := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeOpus()},
			DataReferenceIndex: 1,
		},
		EntryVersion: 0,
		ChannelCount: 2,
		SampleSize:   16,
		SampleRate:   rate << 16,
	}
	if _, err := w.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeOpus()}); err != nil {
		return err
	}
	if _, err := mp4.Marshal(w, ase, mp4.Context{}); err != nil {
		return err
	}
	dops := &mp4.DOps{
		Version:            0,
		OutputChannelCount: 2,
		PreSkip:            312,
		InputSampleRate:    rate,
	}
	if err := writeBox(w, dops, mp4.Context{}); err != nil {
		return err
	}
	if _, err := w.EndBox(); err != nil { // Opus
		return err
	}
	_, err := w.EndBox() // stsd
	return err
}
