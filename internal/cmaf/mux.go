// Package cmaf parses and writes the restricted CMAF/fMP4 shape used by this
// project: one fragmented MP4 per fixture, an init segment with one video and
// one audio track, and self-contained moof+mdat fragment files.
package cmaf

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"rtx/internal/model"

	"github.com/abema/go-mp4"
)

// Codec names understood by the writer. The fixtures use all-intra MJPEG video
// and 16-bit big-endian PCM audio, which FFmpeg can decode without an external
// encoder and which keeps fixture generation pure Go.
const (
	CodecMJPEG = "mp4v/mjpeg"
	CodecPCM   = "ipcm/s16be"
)

// InitSpec describes a generated init segment.
type InitSpec struct {
	VideoTimescale uint32
	AudioTimescale uint32
	Width          uint16
	Height         uint16
	SampleRate     uint32
	Channels       uint16
	MajorBrand     [4]byte
}

// DefaultInitSpec is the layout every fixture uses: 25 fps video on a 90 kHz
// video timescale and 48 kHz mono PCM, so both tracks land on a 720000 Hz
// unified media grid.
func DefaultInitSpec() InitSpec {
	return InitSpec{
		VideoTimescale: 90000,
		AudioTimescale: 48000,
		Width:          320,
		Height:         180,
		SampleRate:     48000,
		Channels:       1,
		MajorBrand:     [4]byte{'i', 's', 'o', 'm'},
	}
}

// MuxSample is one input sample for a generated fragment.
type MuxSample struct {
	Data     []byte
	Duration uint32
	Key      bool
}

// FragmentSpec describes a generated moof+mdat file.
type FragmentSpec struct {
	Sequence uint32
	OmitMFHD bool
	VideoTFD uint64
	AudioTFD uint64
	Video    []MuxSample
	Audio    []MuxSample
}

// sample flag words used by the writer.
const (
	sampleFlagsSync    = 0x02000000 // depends_on = 2 (I-frame)
	sampleFlagsNonSync = 0x01010000 // depends_on = 1, non-sync
)

func box(typ string, payload []byte) []byte {
	b := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[4:8], typ)
	copy(b[8:], payload)
	return b
}

func fullBox(typ string, version uint8, flags uint32, payload []byte) []byte {
	p := make([]byte, 4+len(payload))
	p[0] = version
	p[1] = byte(flags >> 16)
	p[2] = byte(flags >> 8)
	p[3] = byte(flags)
	copy(p[4:], payload)
	return box(typ, p)
}

func u16(v uint16) []byte        { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func u32(v uint32) []byte        { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func u64(v uint64) []byte        { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }
func cat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

func marshal(v mp4.IImmutableBox) []byte {
	var b bytes.Buffer
	if _, err := mp4.Marshal(&b, v, mp4.Context{}); err != nil {
		panic(fmt.Sprintf("cmaf: marshal %s: %v", v.GetType(), err))
	}
	return b.Bytes()
}

func marshalBox(v mp4.IImmutableBox) []byte {
	return box(v.GetType().String(), marshal(v))
}

func entryBox(typ string, e mp4.IImmutableBox, children []byte) []byte {
	return box(typ, cat(marshal(e), children))
}

// mp4vEntry writes the MJPEG sample entry with the esds FFmpeg expects for
// objectTypeIndication 0x6c (MJPEG).
func mp4vEntry(spec InitSpec) []byte {
	e := &mp4.VisualSampleEntry{}
	e.AnyTypeBox.Type = mp4.BoxTypeMp4v()
	e.DataReferenceIndex = 1
	e.Width = spec.Width
	e.Height = spec.Height
	e.Horizresolution = 0x00480000
	e.Vertresolution = 0x00480000
	e.FrameCount = 1
	e.Depth = 0x0018
	e.PreDefined3 = -1
	esdsPayload := cat(
		[]byte{0x03, 0x80, 0x80, 0x80, 0x1b, 0x00, 0x01, 0x00},
		[]byte{0x04, 0x80, 0x80, 0x80, 0x0d, 0x6c, 0x11, 0x00, 0x00, 0x00, 0x00, 0x03, 0x0d, 0x40, 0x00, 0x03, 0x0d, 0x40},
		[]byte{0x06, 0x80, 0x80, 0x80, 0x01, 0x02},
	)
	return entryBox("mp4v", e, fullBox("esds", 0, 0, esdsPayload))
}

func ipcmEntry(spec InitSpec) []byte {
	e := &mp4.AudioSampleEntry{}
	e.AnyTypeBox.Type = mp4.BoxTypeIpcm()
	e.DataReferenceIndex = 1
	e.ChannelCount = spec.Channels
	e.SampleSize = 16
	e.SampleRate = spec.SampleRate << 16
	pcmc := fullBox("pcmC", 0, 0, []byte{0x00, 0x10}) // signed big-endian 16-bit
	btrt := marshalBox(&mp4.Btrt{BufferSizeDB: 0, MaxBitrate: 192000 * 8, AvgBitrate: 192000 * 8})
	return entryBox("ipcm", e, cat(pcmc, btrt))
}

func emptyStblTable() []byte {
	return box("stbl", cat(
		marshalBox(&mp4.Stts{}),
		marshalBox(&mp4.Stsc{}),
		marshalBox(&mp4.Stsz{}),
		marshalBox(&mp4.Stco{}),
	))
}

// BuildInit writes the ftyp + moov for the fixture layout.
func BuildInit(spec InitSpec) ([]byte, error) {
	if spec.VideoTimescale == 0 || spec.AudioTimescale == 0 || spec.SampleRate == 0 {
		return nil, model.E(model.CodeInvalidInput, "build init", "timescales must be positive")
	}
	if spec.Width == 0 || spec.Height == 0 || spec.Channels == 0 {
		return nil, model.E(model.CodeInvalidInput, "build init", "dimensions and channels must be positive")
	}
	ftyp := marshalBox(&mp4.Ftyp{
		MajorBrand:   spec.MajorBrand,
		MinorVersion: 0x200,
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'i', 's', 'o', '6'}},
			{CompatibleBrand: [4]byte{'d', 'a', 's', 'h'}},
		},
	})
	mvhd := marshalBox(&mp4.Mvhd{
		Timescale:   1000,
		Rate:        0x00010000,
		Volume:      0x0100,
		Matrix:      [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000},
		NextTrackID: 3,
	})

	tkhdV := &mp4.Tkhd{TrackID: 1, Volume: 0, Matrix: [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}, Width: uint32(spec.Width) << 16, Height: uint32(spec.Height) << 16}
	tkhdV.SetFlags(3)
	mdhdV := marshalBox(&mp4.Mdhd{Timescale: spec.VideoTimescale, Language: [3]byte{'u', 'n', 'd'}})
	hdlrV := marshalBox(&mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "VideoHandler\x00"})
	vmhd := fullBox("vmhd", 0, 1, cat(u16(0), u16(0), u16(0), u16(0)))
	dref := fullBox("dref", 0, 0, cat(u32(1), fullBox("url ", 0, 1, nil)))
	dinf := box("dinf", dref)
	stsdV := fullBox("stsd", 0, 0, cat(u32(1), mp4vEntry(spec)))
	stblV := box("stbl", cat(stsdV,
		marshalBox(&mp4.Stts{}), marshalBox(&mp4.Stsc{}), marshalBox(&mp4.Stsz{}), marshalBox(&mp4.Stco{})))
	minfV := box("minf", cat(vmhd, dinf, stblV))
	trakV := box("trak", cat(marshalBox(tkhdV), box("mdia", cat(mdhdV, hdlrV, minfV))))

	tkhdA := &mp4.Tkhd{TrackID: 2, Volume: 0x0100, Matrix: [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}}
	tkhdA.SetFlags(3)
	mdhdA := marshalBox(&mp4.Mdhd{Timescale: spec.AudioTimescale, Language: [3]byte{'u', 'n', 'd'}})
	hdlrA := marshalBox(&mp4.Hdlr{HandlerType: [4]byte{'s', 'o', 'u', 'n'}, Name: "SoundHandler\x00"})
	smhd := fullBox("smhd", 0, 0, cat(u16(0), u16(0)))
	stsdA := fullBox("stsd", 0, 0, cat(u32(1), ipcmEntry(spec)))
	stblA := box("stbl", cat(stsdA,
		marshalBox(&mp4.Stts{}), marshalBox(&mp4.Stsc{}), marshalBox(&mp4.Stsz{}), marshalBox(&mp4.Stco{})))
	minfA := box("minf", cat(smhd, dinf, stblA))
	trakA := box("trak", cat(marshalBox(tkhdA), box("mdia", cat(mdhdA, hdlrA, minfA))))

	trexV := marshalBox(&mp4.Trex{TrackID: 1, DefaultSampleDescriptionIndex: 1})
	trexA := marshalBox(&mp4.Trex{TrackID: 2, DefaultSampleDescriptionIndex: 1})
	moov := box("moov", cat(mvhd, trakV, trakA, box("mvex", cat(trexV, trexA))))
	return cat(ftyp, moov), nil
}

func trun(flags uint32, dataOffset int32, entries []byte, count uint32) []byte {
	return fullBox("trun", 0, flags, cat(u32(count), u32(uint32(dataOffset)), entries))
}

// BuildFragment writes a complete styp+moof+mdat fragment file.
func BuildFragment(spec FragmentSpec) ([]byte, error) {
	if len(spec.Video) == 0 && len(spec.Audio) == 0 {
		return nil, model.E(model.CodeInvalidInput, "build fragment", "fragment has no samples")
	}
	styp := marshalBox(&mp4.Styp{
		MajorBrand: [4]byte{'m', 's', 'd', 'h'},
		CompatibleBrands: []mp4.CompatibleBrandElem{
			{CompatibleBrand: [4]byte{'m', 's', 'd', 'h'}},
			{CompatibleBrand: [4]byte{'m', 's', 'i', 'x'}},
		},
	})

	videoVals := make([]byte, 0, len(spec.Video)*12)
	for _, s := range spec.Video {
		flags := uint32(sampleFlagsNonSync)
		if s.Key {
			flags = sampleFlagsSync
		}
		videoVals = append(videoVals, u32(s.Duration)...)
		videoVals = append(videoVals, u32(uint32(len(s.Data)))...)
		videoVals = append(videoVals, u32(flags)...)
	}
	audioVals := make([]byte, 0, len(spec.Audio)*8)
	for _, s := range spec.Audio {
		audioVals = append(audioVals, u32(s.Duration)...)
		audioVals = append(audioVals, u32(uint32(len(s.Data)))...)
	}

	build := func(vOff, aOff int32) []byte {
		var parts [][]byte
		if !spec.OmitMFHD {
			parts = append(parts, fullBox("mfhd", 0, 0, u32(spec.Sequence)))
		}
		if len(spec.Video) > 0 {
			tfhd := fullBox("tfhd", 0, mp4.TfhdDefaultBaseIsMoof, u32(1))
			tfdt := fullBox("tfdt", 1, 0, u64(spec.VideoTFD))
			t := trun(0x000701, vOff, videoVals, uint32(len(spec.Video)))
			parts = append(parts, box("traf", cat(tfhd, tfdt, t)))
		}
		if len(spec.Audio) > 0 {
			tfhd := fullBox("tfhd", 0, mp4.TfhdDefaultBaseIsMoof, u32(2))
			tfdt := fullBox("tfdt", 1, 0, u64(spec.AudioTFD))
			t := trun(0x000301, aOff, audioVals, uint32(len(spec.Audio)))
			parts = append(parts, box("traf", cat(tfhd, tfdt, t)))
		}
		return box("moof", cat(parts...))
	}

	moofLen := len(build(0, 0))
	audioLen := 0
	for _, s := range spec.Audio {
		audioLen += len(s.Data)
	}
	aOff := int32(moofLen + 8)
	vOff := int32(moofLen + 8 + audioLen)
	moof := build(vOff, aOff)

	mdatPayload := make([]byte, 0, audioLen+len(videoVals))
	for _, s := range spec.Audio {
		mdatPayload = append(mdatPayload, s.Data...)
	}
	for _, s := range spec.Video {
		mdatPayload = append(mdatPayload, s.Data...)
	}
	return cat(styp, moof, box("mdat", mdatPayload)), nil
}
