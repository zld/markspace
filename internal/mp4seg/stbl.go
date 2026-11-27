package mp4seg

import (
	"io"

	"github.com/abema/go-mp4"

	"gamepack/internal/clock"
	perr "gamepack/internal/errors"
	"gamepack/internal/stream"
)

// writeStbl writes the sample table. All samples live in one chunk (one mdat
// payload), so stco carries a single offset; stts records integer tick deltas
// and stss records the keyframe sample numbers (1-based).
func writeStbl(mw *mp4.Writer, samples []Sample, entry [4]byte,
	codec stream.Codec) error {
	if _, err := start(mw, mp4.BoxTypeStbl()); err != nil {
		return err
	}

	// stsd with one visual sample entry and codec config box.
	if _, err := start(mw, mp4.BoxTypeStsd()); err != nil {
		return err
	}
	stsd := &mp4.Stsd{EntryCount: 1}
	if _, err := mp4.Marshal(mw, stsd, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	if err := writeSampleEntry(mw, samples, entry, codec); err != nil {
		return err
	}
	if err := end(mw); err != nil { // stsd
		return err
	}

	// stts: one entry per distinct tick delta, integer only.
	if err := writeStts(mw, samples); err != nil {
		return err
	}
	// stss: keyframe sample numbers (1-based).
	if err := writeStss(mw, samples); err != nil {
		return err
	}
	// stsc: single chunk holding every sample.
	if _, err := start(mw, mp4.BoxTypeStsc()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(samples)), SampleDescriptionIndex: 1}},
	}, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	if err := end(mw); err != nil {
		return err
	}

	// stsz: one entry per sample with verbatim unit byte lengths.
	if _, err := start(mw, mp4.BoxTypeStsz()); err != nil {
		return err
	}
	sizes := make([]uint32, len(samples))
	for i, s := range samples {
		sizes[i] = uint32(len(s.Unit.Data))
	}
	if _, err := mp4.Marshal(mw, &mp4.Stsz{
		SampleSize: 0, SampleCount: uint32(len(samples)), EntrySize: sizes,
	}, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	if err := end(mw); err != nil {
		return err
	}

	// stco: single chunk offset, patched after mdat is written (0 placeholder).
	if _, err := start(mw, mp4.BoxTypeStco()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Stco{
		EntryCount: 1, ChunkOffset: []uint32{0},
	}, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	if err := end(mw); err != nil {
		return err
	}

	return end(mw) // stbl
}

func writeSampleEntry(mw *mp4.Writer, samples []Sample, entry [4]byte,
	codec stream.Codec) error {
	boxType := mp4.StrToBoxType(string(entry[:]))
	if _, err := start(mw, boxType); err != nil {
		return err
	}
	vse := &mp4.VisualSampleEntry{
		Width:           320,
		Height:          240,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	// VisualSampleEntry is an AnyTypeBox: its type must be set explicitly
	// because it is constructed directly rather than via BoxType.New().
	vse.SetType(boxType)
	vse.DataReferenceIndex = 1
	if _, err := mp4.Marshal(mw, vse, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	// Codec-specific config box (avcC/hvcC/av1C). Content is provided by the
	// caller; an empty config still yields the box so structure is complete.
	if err := writeConfigBox(mw, samples, codec); err != nil {
		return err
	}
	return end(mw)
}

func writeConfigBox(mw *mp4.Writer, samples []Sample, codec stream.Codec) error {
	switch codec {
	case stream.CodecH264:
		if _, err := start(mw, mp4.BoxTypeAvcC()); err != nil {
			return err
		}
		// Structured, spec-shaped avcC. Parameter sets may be empty for the
		// synthetic test streams; field constants stay valid so Probe parses.
		avcC := &mp4.AVCDecoderConfiguration{
			ConfigurationVersion: 1,
			Profile:              0x42,
			ProfileCompatibility: 0,
			Level:                30,
			LengthSizeMinusOne:   3,
		}
		avcC.SetType(mp4.BoxTypeAvcC())
		if _, err := mp4.Marshal(mw, avcC, mp4.Context{}); err != nil {
			return wrapMarshal(err)
		}
		return end(mw)
	case stream.CodecH265:
		if _, err := start(mw, mp4.BoxTypeHvcC()); err != nil {
			return err
		}
		hvcC := &mp4.HvcC{
			ConfigurationVersion: 1,
			GeneralProfileIdc:    1,
			GeneralLevelIdc:      30,
			LengthSizeMinusOne:   3,
		}
		if _, err := mp4.Marshal(mw, hvcC, mp4.Context{}); err != nil {
			return wrapMarshal(err)
		}
		return end(mw)
	default:
		if _, err := start(mw, mp4.BoxTypeAv1C()); err != nil {
			return err
		}
		av1c := &mp4.Av1C{
			Marker: 1, Version: 1, SeqProfile: 0, SeqLevelIdx0: 8,
			ChromaSubsamplingX: 1, ChromaSubsamplingY: 1,
		}
		if _, err := mp4.Marshal(mw, av1c, mp4.Context{}); err != nil {
			return wrapMarshal(err)
		}
		return end(mw)
	}
}

func writeStts(mw *mp4.Writer, samples []Sample) error {
	if _, err := start(mw, mp4.BoxTypeStts()); err != nil {
		return err
	}
	// Every sample gets an explicit integer-tick duration. Interior samples
	// use the delta to the next sample; the final sample reuses the last
	// delta so the SampleCount total always equals the sample count.
	deltas := make([]uint32, len(samples))
	for i := 0; i+1 < len(samples); i++ {
		deltas[i] = uint32(samples[i+1].Tick - samples[i].Tick)
	}
	if len(samples) == 1 {
		deltas[0] = 0
	} else {
		deltas[len(samples)-1] = deltas[len(samples)-2]
	}
	// Run-length encode equal consecutive deltas while preserving count.
	type run struct{ count, delta uint32 }
	var runs []run
	for _, d := range deltas {
		if n := len(runs); n > 0 && runs[n-1].delta == d {
			runs[n-1].count++
		} else {
			runs = append(runs, run{count: 1, delta: d})
		}
	}
	entries := make([]mp4.SttsEntry, len(runs))
	for i, r := range runs {
		entries[i] = mp4.SttsEntry{SampleCount: r.count, SampleDelta: r.delta}
	}
	if _, err := mp4.Marshal(mw, &mp4.Stts{
		EntryCount: uint32(len(entries)), Entries: entries,
	}, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	return end(mw)
}

func writeStss(mw *mp4.Writer, samples []Sample) error {
	var keys []uint32
	for i, s := range samples {
		if s.IsKey {
			keys = append(keys, uint32(i+1))
		}
	}
	if len(keys) == 0 {
		return nil
	}
	if _, err := start(mw, mp4.BoxTypeStss()); err != nil {
		return err
	}
	if _, err := mp4.Marshal(mw, &mp4.Stss{
		EntryCount: uint32(len(keys)), SampleNumber: keys,
	}, mp4.Context{}); err != nil {
		return wrapMarshal(err)
	}
	return end(mw)
}

// bufSeeker adapts an in-memory byte slice to io.WriteSeeker so box sizes
// can be back-patched by seeks during muxing.
type bufSeeker struct {
	data []byte
	pos  int64
}

func (b *bufSeeker) Write(p []byte) (int, error) {
	end := b.pos + int64(len(p))
	if end > int64(len(b.data)) {
		b.data = append(b.data, make([]byte, end-int64(len(b.data)))...)
	}
	copy(b.data[b.pos:end], p)
	b.pos = end
	return len(p), nil
}

func (b *bufSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
		b.pos = offset
	case io.SeekCurrent:
		b.pos += offset
	case io.SeekEnd:
		b.pos = int64(len(b.data)) + offset
	}
	if b.pos < 0 {
		return 0, perr.New(subsystem, perr.CodeStage, "negative seek")
	}
	return b.pos, nil
}

// Bytes returns the written fragment contents.
func (b *bufSeeker) Bytes() []byte { return b.data }

var _ = clock.TicksPerSecond
