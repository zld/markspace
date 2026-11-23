package demux

import (
	"github.com/abema/go-mp4"
)

func writeVideoStsd(w *mp4.Writer, stbl *mp4.BoxInfo, spec FixtureSpec) error {
	stsd, err := startChild(w, stbl.Context, mp4.BoxTypeStsd())
	if err != nil {
		return err
	}
	if err := marshalBox(w, stsd, &mp4.Stsd{EntryCount: 1}); err != nil {
		return err
	}
	avc1, err := startChild(w, stsd.Context, mp4.BoxTypeAvc1())
	if err != nil {
		return err
	}
	entry := &mp4.VisualSampleEntry{
		Width:           spec.Video.Width,
		Height:          spec.Video.Height,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	entry.DataReferenceIndex = 1
	entry.SetType(mp4.BoxTypeAvc1())
	if err := marshalBox(w, avc1, entry); err != nil {
		return err
	}
	if spec.Video.Color != nil {
		if err := writeColr(w, avc1, spec.Video.Color); err != nil {
			return err
		}
	}
	// A color-conflict fixture gives the bitstream description different values.
	if spec.Fault == FaultColorConflict && spec.Video.BitstreamColor != nil {
		if err := writeColr(w, avc1, spec.Video.BitstreamColor); err != nil {
			return err
		}
	}
	if err := endBoxes(w, 2); err != nil { // avc1, stsd
		return err
	}
	return nil
}

func writeAudioStsd(w *mp4.Writer, stbl *mp4.BoxInfo, s AudioSpec) error {
	stsd, err := startChild(w, stbl.Context, mp4.BoxTypeStsd())
	if err != nil {
		return err
	}
	if err := marshalBox(w, stsd, &mp4.Stsd{EntryCount: 1}); err != nil {
		return err
	}
	mp4a, err := startChild(w, stsd.Context, mp4.BoxTypeMp4a())
	if err != nil {
		return err
	}
	// The mp4a channel count stays conventional; mdst carries the real layout
	// (including 5.1 and the unsupported-layout fault).
	entry := &mp4.AudioSampleEntry{
		ChannelCount: 2,
		SampleSize:   16,
		SampleRate:   uint32(s.SampleRate) << 16,
	}
	entry.DataReferenceIndex = 1
	entry.SetType(mp4.BoxTypeMp4a())
	if err := marshalBox(w, mp4a, entry); err != nil {
		return err
	}
	if err := endBoxes(w, 2); err != nil { // mp4a, stsd
		return err
	}
	return nil
}

func writeColr(w *mp4.Writer, parent *mp4.BoxInfo, c *ColorValue) error {
	return writeFullBox(w, parent.Context, mp4.BoxTypeColr(), &mp4.Colr{
		ColourType:              [4]byte{'n', 'c', 'l', 'x'},
		ColourPrimaries:         c.Primaries,
		TransferCharacteristics: c.Transfer,
		MatrixCoefficients:      c.Matrix,
		FullRangeFlag:           c.FullRange,
	})
}

// writeCommonTables writes stts, stsc, stsz and stco. Chunk offsets are
// placeholders because the kernel never reads media bytes; the structural
// tables still describe every sample exactly.
func writeCommonTables(w *mp4.Writer, stbl *mp4.BoxInfo, frameCount, delta uint32,
	dataBytes uint64, sampleSize uint32) error {
	if err := writeFullBox(w, stbl.Context, mp4.BoxTypeStts(), &mp4.Stts{
		EntryCount: 1,
		Entries:    []mp4.SttsEntry{{SampleCount: frameCount, SampleDelta: delta}},
	}); err != nil {
		return err
	}
	if err := writeFullBox(w, stbl.Context, mp4.BoxTypeStsc(), &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: frameCount, SampleDescriptionIndex: 1}},
	}); err != nil {
		return err
	}
	if err := writeFullBox(w, stbl.Context, mp4.BoxTypeStsz(), &mp4.Stsz{
		SampleSize: sampleSize, SampleCount: frameCount,
	}); err != nil {
		return err
	}
	if err := writeFullBox(w, stbl.Context, mp4.BoxTypeStco(), &mp4.Stco{
		EntryCount: 1, ChunkOffset: []uint32{0},
	}); err != nil {
		return err
	}
	_ = dataBytes
	return nil
}

func writeStss(w *mp4.Writer, stbl *mp4.BoxInfo, frameCount, every uint32, fault FaultCase) error {
	var nums []uint32
	for n := uint32(1); n <= frameCount; n += every {
		nums = append(nums, n)
	}
	if fault == FaultMissingKeyframe {
		// Keep only the first sync sample: no interior keyframes, so no
		// switchable boundary can satisfy the multi-bitrate alignment rule.
		nums = nums[:1]
	}
	return writeFullBox(w, stbl.Context, mp4.BoxTypeStss(), &mp4.Stss{
		EntryCount: uint32(len(nums)), SampleNumber: nums,
	})
}

// writeMdst emits the private "mdst" box with a fixed 8-byte payload.
func writeMdst(w *mp4.Writer, stbl *mp4.BoxInfo, kind byte, layout uint8, rate uint16,
	colorSrc byte, cp uint16) error {
	box := &MdstBox{}
	box.SetType(mp4.StrToBoxType(mdstBoxType))
	copy(box.Payload[:], mdstPayload(kind, layout, rate, colorSrc, cp))
	return writeFullBox(w, stbl.Context, mp4.StrToBoxType(mdstBoxType), box)
}
