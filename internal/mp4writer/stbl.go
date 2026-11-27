package mp4writer

import (
	"github.com/abema/go-mp4"

	"media-check/internal/fixture"
)

// writeStbl writes the sample table with the measured chunk offset.
func writeStbl(b *boxBuilder, tk TrackInput, chunkOffset uint32) error {
	tr := tk.Report
	if err := b.start(mp4.BoxTypeStbl()); err != nil {
		return err
	}
	if err := writeStsd(b, tk); err != nil {
		return err
	}
	if err := writeStts(b, tk); err != nil {
		return err
	}
	if tr.Kind == fixture.TrackVideo {
		if err := writeStss(b, tk); err != nil {
			return err
		}
	}
	if err := writeStsc(b, tk); err != nil {
		return err
	}
	if err := writeStsz(b, tk); err != nil {
		return err
	}
	if err := writeStco(b, chunkOffset); err != nil {
		return err
	}
	return b.end() // stbl
}

func writeStsd(b *boxBuilder, tk TrackInput) error {
	if err := b.start(mp4.BoxTypeStsd()); err != nil {
		return err
	}
	if err := b.payload(&mp4.Stsd{EntryCount: 1}); err != nil {
		return err
	}
	var err error
	if tk.Report.Kind == fixture.TrackVideo {
		err = writeAvc1(b, tk)
	} else {
		err = writeMp4a(b, tk)
	}
	if err != nil {
		return err
	}
	return b.end() // stsd
}

func writeAvc1(b *boxBuilder, tk TrackInput) error {
	if err := b.start(mp4.BoxTypeAvc1()); err != nil {
		return err
	}
	// VisualSampleEntry is an AnyTypeBox: its Type must be set explicitly so
	// Marshal can resolve the avc1 box definition.
	avc1 := &mp4.VisualSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:        mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()},
			DataReferenceIndex: 1,
		},
		Width:           tk.Width,
		Height:          tk.Height,
		Horizresolution: 0x00480000, // 72 dpi
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	if err := b.payload(avc1); err != nil {
		return err
	}
	// Minimal avcC: baseline profile/level, length size 4, no real SPS/PPS
	// bytes are needed for structural Probe round-tripping in this synthetic
	// pipeline; counts stay zero.
	if err := b.start(mp4.BoxTypeAvcC()); err != nil {
		return err
	}
	// avcC is an AnyTypeBox and needs its explicit box type.
	avcC := &mp4.AVCDecoderConfiguration{
		AnyTypeBox:           mp4.AnyTypeBox{Type: mp4.BoxTypeAvcC()},
		ConfigurationVersion: 1,
		Profile:              66, // baseline
		ProfileCompatibility: 0xC0,
		Level:                30,
		LengthSizeMinusOne:   3, // 4-byte NAL length prefix
	}
	if err := b.payload(avcC); err != nil {
		return err
	}
	if err := b.end(); err != nil { // avcC
		return err
	}
	return b.end() // avc1
}

func writeMp4a(b *boxBuilder, tk TrackInput) error {
	if err := b.start(mp4.BoxTypeMp4a()); err != nil {
		return err
	}
	// AudioSampleEntry is likewise an AnyTypeBox needing its mp4a Type.
	mp4a := &mp4.AudioSampleEntry{
		SampleEntry: mp4.SampleEntry{
			AnyTypeBox:         mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()},
			DataReferenceIndex: 1,
		},
		ChannelCount: tk.Channels,
		SampleSize:   16,
		SampleRate:   tk.SampleRate << 16, // fixed-point 16.16
	}
	if err := b.payload(mp4a); err != nil {
		return err
	}
	if err := writeEsds(b); err != nil {
		return err
	}
	return b.end() // mp4a
}

// writeEsds writes a minimal AAC-LC (OTI 0x40) elementary stream descriptor.
func writeEsds(b *boxBuilder) error {
	if err := b.start(mp4.BoxTypeEsds()); err != nil {
		return err
	}
	// Descriptor sizes are explicit: leaf sizes equal their payload length,
	// parent sizes include each nested descriptor's tag + varint size + body.
	const (
		decSpecificLen = 8
		slConfigLen    = 1
		// DecoderConfig body: 1 (OTI) + 1 (stream flags) + 3 (buffer) +
		// 4 max + 4 avg bitrate = 13, plus nested DecSpecific descriptor.
		decoderConfigBody = 13
		// ES_Descriptor body is ESID(2) + flags(1) = 3 bytes.
		esBody = 3
	)
	decSpecific := mp4.Descriptor{Tag: mp4.DecSpecificInfoTag, Size: decSpecificLen,
		Data: []byte{0x12, 0x10, 0, 0, 0, 0, 0, 0}}
	slConfig := mp4.Descriptor{Tag: mp4.SLConfigDescrTag, Size: slConfigLen,
		Data: []byte{0x02}}
	decoderConfig := mp4.Descriptor{
		Tag:  mp4.DecoderConfigDescrTag,
		Size: decoderConfigBody + nestedLen(decSpecific),
		DecoderConfigDescriptor: &mp4.DecoderConfigDescriptor{
			ObjectTypeIndication: 0x40, // AAC
			StreamType:           0x15, // audio
			BufferSizeDB:         0,
			MaxBitrate:           64000,
			AvgBitrate:           64000,
		},
	}
	esDesc := mp4.Descriptor{
		Tag:  mp4.ESDescrTag,
		Size: esBody + nestedLen(decoderConfig) + nestedLen(slConfig),
		ESDescriptor: &mp4.ESDescriptor{
			ESID: 2, StreamPriority: 0,
		},
	}
	esds := &mp4.Esds{
		Descriptors: []mp4.Descriptor{esDesc, decoderConfig, decSpecific, slConfig},
	}
	if err := b.payload(esds); err != nil {
		return err
	}
	return b.end()
}

// nestedLen returns the on-wire size of a child descriptor (tag byte,
// varint size and body). All sizes here fit in one varint byte.
func nestedLen(d mp4.Descriptor) uint32 {
	return 1 + 1 + d.Size
}

// writeStts uses one run of identical deltas in the track timescale.
func writeStts(b *boxBuilder, tk TrackInput) error {
	tr := tk.Report
	if err := b.start(mp4.BoxTypeStts()); err != nil {
		return err
	}
	delta := uint32(0)
	if len(tr.Samples) >= 2 {
		ticks, _, err := tr.Samples[1].Time.ToTicks(int64(tr.ClockRate))
		if err == nil {
			t0, _, _ := tr.Samples[0].Time.ToTicks(int64(tr.ClockRate))
			delta = uint32(ticks - t0)
		}
	}
	if delta == 0 {
		// Synthetic fixtures use a fixed per-track tick increment.
		if tr.Kind == fixture.TrackVideo {
			delta = 3000
		} else {
			delta = 2400
		}
	}
	stts := &mp4.Stts{
		EntryCount: 1,
		Entries:    []mp4.SttsEntry{{SampleCount: uint32(len(tr.Samples)), SampleDelta: delta}},
	}
	if err := b.payload(stts); err != nil {
		return err
	}
	return b.end()
}

// writeStss marks every parsed key frame (1-based sample numbers).
func writeStss(b *boxBuilder, tk TrackInput) error {
	if err := b.start(mp4.BoxTypeStss()); err != nil {
		return err
	}
	var nums []uint32
	for i, s := range tk.Report.Samples {
		if s.IsKey {
			nums = append(nums, uint32(i+1))
		}
	}
	if err := b.payload(&mp4.Stss{EntryCount: uint32(len(nums)), SampleNumber: nums}); err != nil {
		return err
	}
	return b.end()
}

// writeStsc puts all samples in one chunk.
func writeStsc(b *boxBuilder, tk TrackInput) error {
	if err := b.start(mp4.BoxTypeStsc()); err != nil {
		return err
	}
	stsc := &mp4.Stsc{
		EntryCount: 1,
		Entries: []mp4.StscEntry{{
			FirstChunk: 1, SamplesPerChunk: uint32(len(tk.Report.Samples)),
			SampleDescriptionIndex: 1,
		}},
	}
	if err := b.payload(stsc); err != nil {
		return err
	}
	return b.end()
}

func writeStsz(b *boxBuilder, tk TrackInput) error {
	if err := b.start(mp4.BoxTypeStsz()); err != nil {
		return err
	}
	sizes := make([]uint32, len(tk.SampleData))
	for i, d := range tk.SampleData {
		sizes[i] = uint32(len(d))
	}
	stsz := &mp4.Stsz{
		SampleSize: 0, SampleCount: uint32(len(sizes)), EntrySize: sizes,
	}
	if err := b.payload(stsz); err != nil {
		return err
	}
	return b.end()
}

// writeStco emits the single measured chunk offset for this track.
func writeStco(b *boxBuilder, chunkOffset uint32) error {
	if err := b.start(mp4.BoxTypeStco()); err != nil {
		return err
	}
	stco := &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{chunkOffset}}
	if err := b.payload(stco); err != nil {
		return err
	}
	return b.end()
}
