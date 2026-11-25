package fixture

import (
	"github.com/abema/go-mp4"

	"github.com/zld/media-regress/internal/mp4ext"
)

// This file builds the track sample entries. The video track deliberately has
// two avc1 entries that disagree on colour and clean aperture, so the
// normalizer has real conflicts to count.

// buildMP4VideoStsd emits two avc1 entries. The second disagrees on colour and
// clean aperture, which the normalizer must count as two conflicts while the
// first entry stays authoritative.
func buildMP4VideoStsd() (mp4Box, error) {
	first, err := visualEntry(&mp4.Colr{
		ColourType: [4]byte{'n', 'c', 'l', 'x'}, ColourPrimaries: 1,
		TransferCharacteristics: 1, MatrixCoefficients: 1, FullRangeFlag: true,
	}, &mp4ext.Clap{
		CleanApertureWidthN: VideoWidth, CleanApertureWidthD: 1,
		CleanApertureHeightN: VideoHeight, CleanApertureHeightD: 1,
		HorizOffD: 1, VertOffD: 1,
	})
	if err != nil {
		return nil, err
	}
	second, err := visualEntry(&mp4.Colr{
		ColourType: [4]byte{'n', 'c', 'l', 'x'}, ColourPrimaries: 9,
		TransferCharacteristics: 16, MatrixCoefficients: 9,
	}, &mp4ext.Clap{
		CleanApertureWidthN: 1904, CleanApertureWidthD: 1,
		CleanApertureHeightN: 1072, CleanApertureHeightD: 1,
		HorizOffN: 8, HorizOffD: 1, VertOffN: 4, VertOffD: 1,
	})
	if err != nil {
		return nil, err
	}
	payload := append(u32(2), first...)
	payload = append(payload, second...)
	return fullBox("stsd", 0, 0, payload), nil
}

func visualEntry(colr *mp4.Colr, clap *mp4ext.Clap) (mp4Box, error) {
	entry := &mp4.VisualSampleEntry{
		SampleEntry:     mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.BoxTypeAvc1()}, DataReferenceIndex: 1},
		Width:           VideoWidth,
		Height:          VideoHeight,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	payload := marshalPayload(entry)
	pasp := marshalBox(&mp4.PixelAspectRatioBox{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.BoxTypePasp()}, HSpacing: 1, VSpacing: 1})
	payload = append(payload, marshalBox(colr)...)
	payload = append(payload, marshalBox(clap)...)
	payload = append(payload, pasp...)
	return rawBox("avc1", payload), nil
}

func buildMP4AudioTrak(s *spec, ess *mp4Essence, blocks []Block) (mp4Box, error) {
	tkhd := marshalBox(&mp4.Tkhd{
		TrackID:    AudioTrackID,
		DurationV0: VideoFrames * 1000 / FPS,
		Volume:     0x0100,
		Matrix:     identityMatrix,
	})
	mdhd := marshalBox(&mp4.Mdhd{Timescale: AudioRate, DurationV0: AudioFrames, Language: [3]byte{'u', 'n', 'd'}})
	hdlr := marshalBox(&mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "SoundHandler\x00"})
	smhd := fullBox("smhd", 0, 0, []byte{0, 0, 0, 0})
	dinf := containerBox("dinf", fullBox("dref", 0, 0, append(u32(1), fullBox("url ", 0, 1, nil)...)))
	mp4a := marshalBox(&mp4.AudioSampleEntry{
		SampleEntry:  mp4.SampleEntry{AnyTypeBox: mp4.AnyTypeBox{Type: mp4.BoxTypeMp4a()}, DataReferenceIndex: 1},
		ChannelCount: AudioChannels,
		SampleSize:   16,
		SampleRate:   AudioRate << 16,
	})
	sizes := make([]uint32, len(blocks))
	offsets := make([]uint32, len(blocks))
	for i, b := range blocks {
		sizes[i] = uint32(b.Length)
		offsets[i] = uint32(b.Offset)
	}
	bcrc := make([]byte, 0, 4+4*len(ess.crcs))
	bcrc = append(bcrc, u32(uint32(len(ess.crcs)))...)
	for _, c := range ess.crcs {
		bcrc = append(bcrc, u32(c)...)
	}
	stts := marshalBox(&mp4.Stts{EntryCount: 1, Entries: []mp4.SttsEntry{{SampleCount: AudioBlocks, SampleDelta: AudioBlockFrame}}})
	if s.truncateTimestamp {
		// Declare two timing entries but write only one. Readers must report a
		// truncated timestamp table instead of inventing a duration.
		stts = fullBox("stts", 0, 0, append(append(u32(2), u32(AudioBlocks-1)...), u32(AudioBlockFrame)...))
	}
	stbl := containerBox("stbl",
		fullBox("stsd", 0, 0, append(u32(1), mp4a...)),
		stts,
		marshalBox(&mp4.Stsc{EntryCount: 1, Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}}}),
		marshalBox(&mp4.Stsz{SampleCount: uint32(len(blocks)), EntrySize: sizes}),
		marshalBox(&mp4.Stco{EntryCount: uint32(len(blocks)), ChunkOffset: offsets}),
		fullBox("bcrc", 0, 0, bcrc),
	)
	minf := containerBox("minf", smhd, dinf, stbl)
	mdia := containerBox("mdia", mdhd, hdlr, minf)
	return containerBox("trak", tkhd, mdia), nil
}
