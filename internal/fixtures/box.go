package fixtures

import (
	"encoding/binary"
	"fmt"

	"github.com/abema/go-mp4"
)

// box wraps payload in an ISO-BMFF box with an 8-byte header.
func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

// fullBoxPayload returns the 4-byte FullBox prefix followed by rest.
func fullBoxPayload(version byte, flags [3]byte, rest []byte) []byte {
	out := make([]byte, 4, 4+len(rest))
	out[0] = version
	copy(out[1:], flags[:])
	return append(out, rest...)
}

func u32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}

func u16(v uint16) []byte {
	var b [2]byte
	binary.BigEndian.PutUint16(b[:], v)
	return b[:]
}

func cat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// marshalPayload marshals a go-mp4 box struct into payload bytes only. The
// library's Marshal writes fields without the common box header.
func marshalPayload(b mp4.IImmutableBox) ([]byte, error) {
	var w sliceWriter
	if _, err := mp4.Marshal(&w, b, mp4.Context{}); err != nil {
		return nil, err
	}
	return w.buf, nil
}

type sliceWriter struct{ buf []byte }

func (w *sliceWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	return len(p), nil
}

func ftypBox() []byte {
	p, _ := marshalPayload(&mp4.Ftyp{
		MajorBrand:   [4]byte{'i', 's', 'o', '5'},
		MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', '5'}},
			{CompatibleBrand: [4]byte{'m', 'p', '4', '1'}},
			{CompatibleBrand: [4]byte{'a', 'v', 'c', '1'}},
		},
	})
	return box("ftyp", p)
}

func mvhdBox(timescale uint32, duration uint64, nextTrackID uint32) []byte {
	p, _ := marshalPayload(&mp4.Mvhd{
		FullBox:     mp4.FullBox{Version: 0},
		Timescale:   timescale,
		DurationV0:  uint32(duration),
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      identityMatrix(),
		NextTrackID: nextTrackID,
	})
	return box("mvhd", p)
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

func rotationMatrix(deg int) [9]int32 {
	switch deg {
	case 90:
		return [9]int32{0, 0x00010000, 0, -0x00010000, 0, 0, 0, 0, 0x40000000}
	case 180:
		return [9]int32{-0x00010000, 0, 0, 0, -0x00010000, 0, 0, 0, 0x40000000}
	case 270:
		return [9]int32{0, -0x00010000, 0, 0x00010000, 0, 0, 0, 0, 0x40000000}
	default:
		return identityMatrix()
	}
}

func tkhdBox(trackID uint32, duration uint32, width, height uint16, volume int16, rot int) []byte {
	p, _ := marshalPayload(&mp4.Tkhd{
		FullBox:    mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 7}},
		TrackID:    trackID,
		DurationV0: duration,
		Volume:     volume,
		Matrix:     rotationMatrix(rot),
		Width:      uint32(width) << 16,
		Height:     uint32(height) << 16,
	})
	return box("tkhd", p)
}

func mdhdBox(timescale uint32, duration uint32, lang [3]byte) []byte {
	p, _ := marshalPayload(&mp4.Mdhd{
		FullBox:    mp4.FullBox{Version: 0},
		Timescale:  timescale,
		DurationV0: duration,
		Language:   lang,
	})
	return box("mdhd", p)
}

func hdlrBox(handler string, name string) []byte {
	p, _ := marshalPayload(&mp4.Hdlr{
		FullBox:     mp4.FullBox{Version: 0},
		HandlerType: [4]byte{handler[0], handler[1], handler[2], handler[3]},
		Name:        name,
	})
	return box("hdlr", p)
}

func dinfBox() []byte {
	urlPayload := fullBoxPayload(0, [3]byte{0, 0, 1}, nil)
	url := box("url ", urlPayload)
	drefPayload := fullBoxPayload(0, [3]byte{0, 0, 0}, cat(u32(1), url))
	return box("dinf", box("dref", drefPayload))
}

func sttsBox(count uint32, delta uint32) []byte {
	p, _ := marshalPayload(&mp4.Stts{
		FullBox:    mp4.FullBox{Version: 0},
		EntryCount: 1,
		Entries:    []mp4.SttsEntry{{SampleCount: count, SampleDelta: delta}},
	})
	return box("stts", p)
}

func stscBox() []byte {
	p, _ := marshalPayload(&mp4.Stsc{
		FullBox:    mp4.FullBox{Version: 0},
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: 1, SampleDescriptionIndex: 1}},
	})
	return box("stsc", p)
}

func stszBox(sizes []uint32) []byte {
	p, _ := marshalPayload(&mp4.Stsz{
		FullBox:     mp4.FullBox{Version: 0},
		SampleSize:  0,
		SampleCount: uint32(len(sizes)),
		EntrySize:   sizes,
	})
	return box("stsz", p)
}

func stcoBox(offsets []uint32) []byte {
	p, _ := marshalPayload(&mp4.Stco{
		FullBox:     mp4.FullBox{Version: 0},
		EntryCount:  uint32(len(offsets)),
		ChunkOffset: offsets,
	})
	return box("stco", p)
}

func stssBox(numbers []uint32) []byte {
	p, _ := marshalPayload(&mp4.Stss{
		FullBox:      mp4.FullBox{Version: 0},
		EntryCount:   uint32(len(numbers)),
		SampleNumber: numbers,
	})
	return box("stss", p)
}

func colrBox(c Color) []byte {
	p := make([]byte, 0, 11)
	p = append(p, 'n', 'c', 'l', 'x')
	p = append(p, u16(c.Primaries)...)
	p = append(p, u16(c.Transfer)...)
	p = append(p, u16(c.Matrix)...)
	var full byte
	if c.FullRange {
		full = 0x80
	}
	p = append(p, full)
	return box("colr", p)
}

func clapBox(crop Crop, codedW, codedH uint16) []byte {
	apW := int32(codedW) - int32(crop.Left) - int32(crop.Right)
	apH := int32(codedH) - int32(crop.Top) - int32(crop.Bottom)
	var p []byte
	for _, pair := range [][2]int32{
		{apW, 1}, {apH, 1}, {0, 1}, {0, 1},
	} {
		p = append(p, u32(uint32(pair[0]))...)
		p = append(p, u32(uint32(pair[1]))...)
	}
	return box("clap", p)
}

func avcCBox() []byte {
	// Minimal avcC: version 1, baseline profile, one SPS and one PPS.
	sps := []byte{0x67, 0x42, 0x00, 0x0a, 0xaa, 0xbb, 0xcc}
	pps := []byte{0x68, 0xce, 0x38, 0x80}
	p := []byte{1, 0x42, 0x00, 0x0a, 0xff, 0xe1}
	p = append(p, u16(uint16(len(sps)))...)
	p = append(p, sps...)
	p = append(p, 1)
	p = append(p, u16(uint16(len(pps)))...)
	p = append(p, pps...)
	return box("avcC", p)
}

func esdsBox() []byte {
	decoderConfig := cat([]byte{0x04, 0x11, 0x40, 0x15}, []byte{0, 0, 0}, []byte{0, 0, 0, 0}, []byte{0, 0, 0, 0})
	slConfig := []byte{0x06, 0x01, 0x02}
	es := cat([]byte{0x03, byte(3 + len(decoderConfig) + len(slConfig)), 0x00, 0x01, 0x00}, decoderConfig, slConfig)
	return box("esds", fullBoxPayload(0, [3]byte{0, 0, 0}, es))
}

func audioEntryBox() []byte {
	ase := &mp4.AudioSampleEntry{
		SampleEntry:  mp4.SampleEntry{DataReferenceIndex: 1},
		ChannelCount: 2,
		SampleSize:   16,
		SampleRate:   48000 << 16,
	}
	ase.SetType(mp4.BoxTypeMp4a())
	p, _ := marshalPayload(ase)
	return box("mp4a", cat(p, esdsBox()))
}

func videoEntryBox(c Color, crop *Crop, codedW, codedH uint16) []byte {
	vse := &mp4.VisualSampleEntry{
		SampleEntry:     mp4.SampleEntry{DataReferenceIndex: 1},
		Width:           codedW,
		Height:          codedH,
		Horizresolution: 0x00480000,
		Vertresolution:  0x00480000,
		FrameCount:      1,
		Depth:           0x0018,
		PreDefined3:     -1,
	}
	vse.SetType(mp4.BoxTypeAvc1())
	parts := [][]byte{}
	p, _ := marshalPayload(vse)
	parts = append(parts, p, avcCBox(), colrBox(c))
	if crop != nil {
		parts = append(parts, clapBox(*crop, codedW, codedH))
	}
	return box("avc1", cat(parts...))
}

func stsdBox(entry []byte) []byte {
	return box("stsd", cat(fullBoxPayload(0, [3]byte{}, nil), u32(1), entry))
}

// videoStbl builds the video sample table. offsets are absolute file offsets.
func videoStbl(sizes []uint32, offsets []uint32, c Color, crop *Crop, w, h uint16, keys []uint32) []byte {
	parts := [][]byte{
		stsdBox(videoEntryBox(c, crop, w, h)),
		sttsBox(uint32(len(sizes)), 900),
		stscBox(),
		stszBox(sizes),
		stcoBox(offsets),
		stssBox(keys),
	}
	return box("stbl", cat(parts...))
}

func audioStbl(sizes []uint32, offsets []uint32) []byte {
	parts := [][]byte{
		stsdBox(audioEntryBox()),
		sttsBox(uint32(len(sizes)), 480),
		stscBox(),
		stszBox(sizes),
		stcoBox(offsets),
	}
	return box("stbl", cat(parts...))
}

func videoTrak(stbl []byte, duration uint32, w, h uint16, rot int) []byte {
	minf := box("minf", cat(
		box("vmhd", fullBoxPayload(0, [3]byte{0, 0, 1}, []byte{0, 0, 0, 0, 0, 0, 0, 0})),
		dinfBox(), stbl))
	mdia := box("mdia", cat(mdhdBox(90000, duration, [3]byte{'u', 'n', 'd'}), hdlrBox("vide", "VideoHandler"), minf))
	return box("trak", cat(tkhdBox(1, duration/90, w, h, 0, rot), mdia))
}

func audioTrak(stbl []byte, duration uint32) []byte {
	minf := box("minf", cat(
		box("smhd", fullBoxPayload(0, [3]byte{}, []byte{0, 0, 0, 0})),
		dinfBox(), stbl))
	mdia := box("mdia", cat(mdhdBox(48000, duration, [3]byte{'u', 'n', 'd'}), hdlrBox("soun", "SoundHandler"), minf))
	return box("trak", cat(tkhdBox(2, duration/48, 0, 0, 0x0100, 0), mdia))
}

func formatTag(kind, index int) []byte {
	return []byte(fmt.Sprintf("RPLY%02d%02d", kind, index))
}
