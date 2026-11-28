// Package container builds and verifies MP4 containers holding one
// track per codec. Only the explicitly supported constraint set is
// checked: ftyp+moov+mdat layout, per-track mdhd/hdlr/stsd/stts/stsc/
// stsz/stco/stss consistency with the track specs.
package container

import (
	"bytes"
	"encoding/binary"

	mp4 "github.com/abema/go-mp4"

	"mediacheck/internal/codec"
	"mediacheck/internal/fault"
)

// TrackSpec describes one track to embed in the container.
type TrackSpec struct {
	ID          uint32
	Kind        codec.Kind
	Timescale   uint32
	Samples     [][]byte // sample payloads
	SampleDelta uint32   // uniform stts delta in track timescale
	KeySamples  []uint32 // 1-based keyframe sample numbers (stss)
}

// TrackReport summarizes one verified track.
type TrackReport struct {
	ID          uint32
	Kind        codec.Kind
	Timescale   uint32
	SampleCount uint32
	KeyCount    uint32
}

// Report summarizes a verified container.
type Report struct {
	MajorBrand string
	Tracks     []TrackReport
}

var matrix = [9]int32{0x10000, 0, 0, 0, 0x10000, 0, 0, 0, 0x40000000}

func entryType(k codec.Kind) mp4.BoxType {
	switch k {
	case codec.H264:
		return mp4.BoxTypeAvc1()
	case codec.H265:
		return mp4.BoxTypeHvc1()
	default:
		return mp4.BoxTypeAv01()
	}
}

func kindOfEntry(t mp4.BoxType) (codec.Kind, bool) {
	switch t {
	case mp4.BoxTypeAvc1():
		return codec.H264, true
	case mp4.BoxTypeHvc1():
		return codec.H265, true
	case mp4.BoxTypeAv01():
		return codec.AV1, true
	}
	return 0, false
}

func wrap(t mp4.BoxType, p []byte) []byte {
	b := make([]byte, 8+len(p))
	binary.BigEndian.PutUint32(b, uint32(len(b)))
	copy(b[4:], t[:])
	copy(b[8:], p)
	return b
}

func payload(b mp4.IImmutableBox) ([]byte, error) {
	var buf bytes.Buffer
	_, err := mp4.Marshal(&buf, b, mp4.Context{})
	return buf.Bytes(), err
}

// Build serializes an MP4 holding the given tracks. Two passes are
// used so stco chunk offsets account for the final moov size.
func Build(specs []TrackSpec) ([]byte, error) {
	if len(specs) == 0 {
		return nil, fault.New(fault.ErrMissingTrack, fault.NoOffset, "container needs at least one track")
	}
	ftypPayload, err := payload(&mp4.Ftyp{MajorBrand: mp4.BrandISOM(), MinorVersion: 512,
		CompatibleBrands: []mp4.CompatibleBrandElem{{CompatibleBrand: mp4.BrandISOM()}}})
	if err != nil {
		return nil, err
	}
	ftyp := wrap(mp4.BoxTypeFtyp(), ftypPayload)
	moov0, err := buildMoov(specs, 0)
	if err != nil {
		return nil, err
	}
	mdatStart := uint32(len(ftyp) + len(moov0) + 8)
	moov, err := buildMoov(specs, mdatStart)
	if err != nil {
		return nil, err
	}
	if len(moov) != len(moov0) {
		return nil, fault.New(fault.ErrIntegrity, fault.NoOffset, "moov size drifted between passes")
	}
	out := append(append(ftyp, moov...), wrap(mp4.BoxTypeMdat(), joinSamples(specs))...)
	return out, nil
}

func joinSamples(specs []TrackSpec) []byte {
	var out []byte
	for _, s := range specs {
		for _, smp := range s.Samples {
			out = append(out, smp...)
		}
	}
	return out
}

func buildMoov(specs []TrackSpec, mdatStart uint32) ([]byte, error) {
	mvhd, err := payload(&mp4.Mvhd{Timescale: 90000, DurationV0: 0,
		Rate: 0x10000, Volume: 0x100, Matrix: matrix, NextTrackID: uint32(len(specs) + 1)})
	if err != nil {
		return nil, err
	}
	body := wrap(mp4.BoxTypeMvhd(), mvhd)
	chunk := mdatStart
	for _, s := range specs {
		trak, err := buildTrak(s, chunk)
		if err != nil {
			return nil, err
		}
		body = append(body, trak...)
		for _, smp := range s.Samples {
			chunk += uint32(len(smp))
		}
	}
	return wrap(mp4.BoxTypeMoov(), body), nil
}

func buildTrak(s TrackSpec, chunkOff uint32) ([]byte, error) {
	n := uint32(len(s.Samples))
	dur := uint32(0)
	sizes := make([]uint32, n)
	for i, smp := range s.Samples {
		sizes[i] = uint32(len(smp))
		dur += s.SampleDelta
	}
	tkhd, err := payload(&mp4.Tkhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 7}},
		TrackID: s.ID, DurationV0: dur, Matrix: matrix})
	if err != nil {
		return nil, err
	}
	mdhd, err := payload(&mp4.Mdhd{Timescale: s.Timescale, DurationV0: dur,
		Language: [3]byte{'u', 'n', 'd'}})
	if err != nil {
		return nil, err
	}
	hdlr, err := payload(&mp4.Hdlr{HandlerType: [4]byte{'v', 'i', 'd', 'e'}, Name: "media-check"})
	if err != nil {
		return nil, err
	}
	vse := &mp4.VisualSampleEntry{Width: 64, Height: 64, FrameCount: 1, Depth: 24, PreDefined3: -1}
	vse.SetType(entryType(s.Kind))
	vse.DataReferenceIndex = 1
	entry, err := payload(vse)
	if err != nil {
		return nil, err
	}
	stsd, err := payload(&mp4.Stsd{EntryCount: 1})
	if err != nil {
		return nil, err
	}
	stts, err := payload(&mp4.Stts{EntryCount: 1,
		Entries: []mp4.SttsEntry{{SampleCount: n, SampleDelta: s.SampleDelta}}})
	if err != nil {
		return nil, err
	}
	stsc, err := payload(&mp4.Stsc{EntryCount: 1,
		Entries: []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: n, SampleDescriptionIndex: 1}}})
	if err != nil {
		return nil, err
	}
	stsz, err := payload(&mp4.Stsz{SampleCount: n, EntrySize: sizes})
	if err != nil {
		return nil, err
	}
	stco, err := payload(&mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{chunkOff}})
	if err != nil {
		return nil, err
	}
	stss, err := payload(&mp4.Stss{EntryCount: uint32(len(s.KeySamples)), SampleNumber: s.KeySamples})
	if err != nil {
		return nil, err
	}
	entryBox := wrap(entryType(s.Kind), entry)
	stsdBox := wrap(mp4.BoxTypeStsd(), append(stsd, entryBox...))
	stblBody := append(stsdBox, wrap(mp4.BoxTypeStts(), stts)...)
	stblBody = append(stblBody, wrap(mp4.BoxTypeStsc(), stsc)...)
	stblBody = append(stblBody, wrap(mp4.BoxTypeStsz(), stsz)...)
	stblBody = append(stblBody, wrap(mp4.BoxTypeStco(), stco)...)
	stblBody = append(stblBody, wrap(mp4.BoxTypeStss(), stss)...)
	stbl := wrap(mp4.BoxTypeStbl(), stblBody)
	minf := wrap(mp4.BoxTypeMinf(), stbl)
	mdiaBody := append(append(wrap(mp4.BoxTypeMdhd(), mdhd), wrap(mp4.BoxTypeHdlr(), hdlr)...), minf...)
	mdia := wrap(mp4.BoxTypeMdia(), mdiaBody)
	return wrap(mp4.BoxTypeTrak(), append(wrap(mp4.BoxTypeTkhd(), tkhd), mdia...)), nil
}
