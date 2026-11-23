package remux

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"

	"github.com/abema/go-mp4"

	"mediakernel/internal/mediaerr"
)

// writeMinf writes minf > stbl(stsd/stts/stsc/stsz/stco). The sample tables
// describe one chunk holding all samples; offsets point into mdat.
func writeMinf(mw *mp4.Writer, tr Track) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMinf()}); err != nil {
		return wrapMP4(err)
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStbl()}); err != nil {
		return wrapMP4(err)
	}
	if err := writeStsd(mw, tr); err != nil {
		return err
	}
	if err := writeStts(mw, tr); err != nil {
		return err
	}
	if err := writeStsc(mw, tr); err != nil {
		return err
	}
	if err := writeStsz(mw, tr); err != nil {
		return err
	}
	if err := writeStco(mw); err != nil {
		return err
	}
	if _, err := mw.EndBox(); err != nil { // stbl
		return wrapMP4(err)
	}
	_, err := mw.EndBox() // minf
	return wrapMP4(err)
}

func writeStsd(mw *mp4.Writer, tr Track) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsd()}); err != nil {
		return wrapMP4(err)
	}
	stsd := &mp4.Stsd{EntryCount: 1}
	if _, err := mp4.Marshal(mw, stsd, mp4.Context{}); err != nil {
		return wrapMP4(err)
	}
	boxType := mp4.BoxTypeMp4a()
	if tr.Kind == "video" {
		boxType = mp4.BoxTypeAvc1()
	}
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: boxType}); err != nil {
		return wrapMP4(err)
	}
	if tr.Kind == "video" {
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
			Depth:           0x0018,
			PreDefined3:     -1,
		}
		if _, err := mp4.Marshal(mw, entry, mp4.Context{}); err != nil {
			return wrapMP4(err)
		}
		if err := writeMinimalAvcC(mw); err != nil {
			return err
		}
	} else {
		entry := &mp4.AudioSampleEntry{
			SampleEntry: mp4.SampleEntry{
				AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()},
				DataReferenceIndex: 1,
			},
			ChannelCount: tr.ChannelCount,
			SampleSize:   16,
			SampleRate:   tr.SampleRate << 16,
		}
		if _, err := mp4.Marshal(mw, entry, mp4.Context{}); err != nil {
			return wrapMP4(err)
		}
	}
	if _, err := mw.EndBox(); err != nil { // sample entry
		return wrapMP4(err)
	}
	_, err := mw.EndBox() // stsd
	return wrapMP4(err)
}

// writeMinimalAvcC writes a structurally valid but parameter-set-free avcC
// sufficient for box parsing; the synthetic payload is not meant to decode.
func writeMinimalAvcC(mw *mp4.Writer) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeAvcC()}); err != nil {
		return wrapMP4(err)
	}
	avcc := &mp4.AVCDecoderConfiguration{
		AnyTypeBox:           mp4.AnyTypeBox{Type: mp4.BoxTypeAvcC()},
		ConfigurationVersion: 1,
		Profile:              100, // High
		Level:                40,
		LengthSizeMinusOne:   3, // 4-byte NAL length
	}
	if _, err := mp4.Marshal(mw, avcc, mp4.Context{}); err != nil {
		return wrapMP4(err)
	}
	_, err := mw.EndBox()
	return wrapMP4(err)
}

func writeStts(mw *mp4.Writer, tr Track) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStts()}); err != nil {
		return wrapMP4(err)
	}
	stts := &mp4.Stts{
		EntryCount: 1,
		Entries:    []mp4.SttsEntry{{SampleCount: tr.SampleCount, SampleDelta: tr.SampleDelta}},
	}
	if _, err := mp4.Marshal(mw, stts, mp4.Context{}); err != nil {
		return wrapMP4(err)
	}
	_, err := mw.EndBox()
	return wrapMP4(err)
}

func writeStsc(mw *mp4.Writer, tr Track) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsc()}); err != nil {
		return wrapMP4(err)
	}
	stsc := &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: tr.SampleCount, SampleDescriptionIndex: 1}},
	}
	if _, err := mp4.Marshal(mw, stsc, mp4.Context{}); err != nil {
		return wrapMP4(err)
	}
	_, err := mw.EndBox()
	return wrapMP4(err)
}

func writeStsz(mw *mp4.Writer, tr Track) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStsz()}); err != nil {
		return wrapMP4(err)
	}
	sizes := make([]uint32, tr.SampleCount)
	for i := range sizes {
		sizes[i] = uint32(len(tr.Payload))
	}
	stsz := &mp4.Stsz{SampleCount: tr.SampleCount, EntrySize: sizes}
	if _, err := mp4.Marshal(mw, stsz, mp4.Context{}); err != nil {
		return wrapMP4(err)
	}
	_, err := mw.EndBox()
	return wrapMP4(err)
}

// writeStco records a single placeholder chunk offset; mdat is the final box
// and this synthetic layout is verified structurally rather than decoded.
func writeStco(mw *mp4.Writer) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeStco()}); err != nil {
		return wrapMP4(err)
	}
	stco := &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{0}}
	if _, err := mp4.Marshal(mw, stco, mp4.Context{}); err != nil {
		return wrapMP4(err)
	}
	_, err := mw.EndBox()
	return wrapMP4(err)
}

func writeMdat(mw *mp4.Writer, in Input) error {
	if _, err := mw.StartBox(&mp4.BoxInfo{Type: mp4.BoxTypeMdat()}); err != nil {
		return wrapMP4(err)
	}
	for _, tr := range in.Tracks {
		if _, err := mw.Write(tr.Payload); err != nil {
			return wrapMP4(err)
		}
	}
	_, err := mw.EndBox()
	return wrapMP4(err)
}

// verifyAndHash re-opens the file, parses the box tree with go-mp4, checks
// the track/sample counts against the input, then hashes the bytes.
func verifyAndHash(path string, in Input) (*Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, mediaerr.New(mediaerr.CatRemux, "reopen", err.Error()).Wrap(err)
	}
	defer f.Close()

	info, err := mp4.Probe(f)
	if err != nil {
		return nil, mediaerr.New(mediaerr.CatRemux, "verify", "output failed MP4 probe").Wrap(err)
	}
	if len(info.Tracks) != len(in.Tracks) {
		return nil, mediaerr.New(mediaerr.CatRemux, "verify_track",
			"track count mismatch after remux")
	}
	var video, audio uint32
	for _, t := range info.Tracks {
		switch t.Codec {
		case mp4.CodecAVC1:
			video += uint32(len(t.Samples))
		case mp4.CodecMP4A:
			audio += uint32(len(t.Samples))
		}
	}
	wantVideo, wantAudio := sampleExpectations(in)
	if video != wantVideo || audio != wantAudio {
		return nil, mediaerr.New(mediaerr.CatRemux, "verify_samples",
			"sample count mismatch after remux")
	}

	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, wrapMP4(err)
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return nil, mediaerr.New(mediaerr.CatRemux, "hash", err.Error()).Wrap(err)
	}
	st, err := f.Stat()
	if err != nil {
		return nil, wrapMP4(err)
	}
	return &Summary{
		Size: st.Size(), SHA256: hex.EncodeToString(h.Sum(nil)),
		Tracks: len(in.Tracks), VideoSamples: video, AudioSamples: audio,
		DurationTick: in.DurationTick, Verified: true,
	}, nil
}

func sampleExpectations(in Input) (video, audio uint32) {
	for _, tr := range in.Tracks {
		if tr.Kind == "video" {
			video += tr.SampleCount
		} else {
			audio += tr.SampleCount
		}
	}
	return
}

func trackDuration(tr Track) uint32 {
	return tr.SampleCount * tr.SampleDelta
}

func strTo4(s string) [4]byte {
	var b [4]byte
	copy(b[:], s)
	return b
}

func wrapMP4(err error) error {
	if err == nil {
		return nil
	}
	var me *mediaerr.Error
	if e, ok := mediaerr.As(err); ok {
		me = e
	}
	if me != nil {
		return me
	}
	return mediaerr.New(mediaerr.CatRemux, "mp4", err.Error())
}
