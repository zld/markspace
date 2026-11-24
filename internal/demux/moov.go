package demux

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/abema/go-mp4"

	"mediarc/internal/model"
)

func fullBoxPayload(b mp4.IImmutableBox) ([]byte, error) {
	var buf bytes.Buffer
	if _, err := mp4.Marshal(&buf, b, mp4.Context{}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func leafBox(typ mp4.BoxType, b mp4.IImmutableBox) ([]byte, error) {
	payload, err := fullBoxPayload(b)
	if err != nil {
		return nil, fmt.Errorf("marshal %s: %w", typ.String(), err)
	}
	return boxBytes(typ, payload), nil
}

func boxBytes(typ mp4.BoxType, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[:4], uint32(len(out)))
	copy(out[4:8], typ[:])
	copy(out[8:], payload)
	return out
}

func containerBox(typ mp4.BoxType, children ...[]byte) []byte {
	var payload bytes.Buffer
	for _, child := range children {
		payload.Write(child)
	}
	return boxBytes(typ, payload.Bytes())
}

func identityMatrix() [9]int32 {
	return [9]int32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}
}

// buildMoov creates a small but probe-compatible ISO-BMFF movie. The MRCA
// box remains the lossless source-of-truth; these standard tables let
// go-mp4.Probe resolve track IDs, time bases and sample counts.
func buildMoov(a *model.Archive, chunkOffsets []uint32) ([]byte, error) {
	durationMS := uint32(1000)
	for _, tr := range a.Tracks {
		if ms := model.NewTimestamp(tr.DurationTicks, tr.TimeBase).Millis(); ms > int64(durationMS) {
			durationMS = uint32(ms)
		}
	}
	mvhd, err := leafBox(mp4.BoxTypeMvhd(), &mp4.Mvhd{
		Timescale: 1000, DurationV0: durationMS, Rate: 0x00010000,
		Volume: 0x0100, Matrix: identityMatrix(), NextTrackID: uint32(len(a.Tracks) + 1),
	})
	if err != nil {
		return nil, err
	}
	traks := make([][]byte, 0, len(a.Tracks))
	for i, tr := range a.Tracks {
		offset := uint32(0)
		if i < len(chunkOffsets) {
			offset = chunkOffsets[i]
		}
		trak, err := buildTrak(a, tr, offset)
		if err != nil {
			return nil, err
		}
		traks = append(traks, trak)
	}
	return containerBox(mp4.BoxTypeMoov(), append([][]byte{mvhd}, traks...)...), nil
}

func buildTrak(a *model.Archive, tr model.Track, chunkOffset uint32) ([]byte, error) {
	durationMS := uint32(model.NewTimestamp(tr.DurationTicks, tr.TimeBase).Millis())
	tkhd, err := leafBox(mp4.BoxTypeTkhd(), &mp4.Tkhd{
		FullBox:    mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 3}},
		TrackID:    tr.ID,
		DurationV0: durationMS,
		Layer:      0, AlternateGroup: 0,
		Volume: map[bool]int16{true: 0x0100, false: 0}[tr.Type == model.TrackAudio],
		Matrix: identityMatrix(),
		Width:  uint32(tr.Width) << 16,
		Height: uint32(tr.Height) << 16,
	})
	if err != nil {
		return nil, err
	}
	timescale := uint32(tr.TimeBase.Den)
	if timescale == 0 {
		timescale = 1000
	}
	mdhd, err := leafBox(mp4.BoxTypeMdhd(), &mp4.Mdhd{
		Timescale: timescale, DurationV0: uint32(tr.DurationTicks),
	})
	if err != nil {
		return nil, err
	}
	handler := handlerFor(tr.Type)
	hdlr, err := leafBox(mp4.BoxTypeHdlr(), &mp4.Hdlr{HandlerType: handler, Name: string(tr.Type) + " handler"})
	if err != nil {
		return nil, err
	}
	minf, err := buildMinf(tr)
	if err != nil {
		return nil, err
	}
	stbl, err := buildStbl(a, tr, chunkOffset)
	if err != nil {
		return nil, err
	}
	mdia := containerBox(mp4.BoxTypeMdia(), mdhd, hdlr, containerBox(mp4.BoxTypeMinf(), minf, stbl))
	return containerBox(mp4.BoxTypeTrak(), tkhd, mdia), nil
}

func handlerFor(t model.TrackType) [4]byte {
	switch t {
	case model.TrackVideo:
		return [4]byte{'v', 'i', 'd', 'e'}
	case model.TrackAudio:
		return [4]byte{'s', 'o', 'u', 'n'}
	case model.TrackSubtitle:
		return [4]byte{'t', 'e', 'x', 't'}
	case model.TrackChapter:
		return [4]byte{'c', 'h', 'a', 'p'}
	default:
		return [4]byte{'m', 'e', 't', 'a'}
	}
}

func buildMinf(tr model.Track) ([]byte, error) {
	var headerType mp4.BoxType
	var header mp4.IImmutableBox
	switch tr.Type {
	case model.TrackVideo:
		headerType = mp4.BoxTypeVmhd()
		header = &mp4.Vmhd{FullBox: mp4.FullBox{Flags: [3]byte{0, 0, 1}}}
	case model.TrackAudio:
		headerType = mp4.BoxTypeSmhd()
		header = &mp4.Smhd{}
	default:
		nmhd := boxBytes(mp4.BoxType{'n', 'm', 'h', 'd'}, []byte{0, 0, 0, 0})
		dinf, err := buildDataInformation()
		if err != nil {
			return nil, err
		}
		return append(nmhd, dinf...), nil
	}
	h, err := leafBox(headerType, header)
	if err != nil {
		return nil, err
	}
	dinf, err := buildDataInformation()
	if err != nil {
		return nil, err
	}
	return append(h, dinf...), nil
}

func buildDataInformation() ([]byte, error) {
	urlPayload, err := fullBoxPayload(&mp4.Url{FullBox: mp4.FullBox{Version: 0, Flags: [3]byte{0, 0, 1}}})
	if err != nil {
		return nil, err
	}
	urlBox := boxBytes(mp4.BoxTypeUrl(), urlPayload)
	drefPayload, err := fullBoxPayload(&mp4.Dref{EntryCount: 1})
	if err != nil {
		return nil, err
	}
	dref := boxBytes(mp4.BoxTypeDref(), append(drefPayload, urlBox...))
	return containerBox(mp4.BoxTypeDinf(), dref), nil
}

func buildStbl(a *model.Archive, tr model.Track, chunkOffset uint32) ([]byte, error) {
	samples := a.SamplesFor(tr.ID)
	stsd, err := leafBox(mp4.BoxTypeStsd(), &mp4.Stsd{EntryCount: 0})
	if err != nil {
		return nil, err
	}
	delta := uint32(0)
	if len(samples) > 0 {
		delta = uint32(samples[0].Duration)
	}
	stts, err := leafBox(mp4.BoxTypeStts(), &mp4.Stts{
		EntryCount: boolU32(len(samples) > 0),
		Entries:    []mp4.SttsEntry{{SampleCount: uint32(len(samples)), SampleDelta: delta}},
	})
	if err != nil {
		return nil, err
	}
	stsc, err := leafBox(mp4.BoxTypeStsc(), &mp4.Stsc{
		EntryCount: 1,
		Entries:    []mp4.StscEntry{{FirstChunk: 1, SamplesPerChunk: uint32(len(samples)), SampleDescriptionIndex: 1}},
	})
	if err != nil {
		return nil, err
	}
	sizes := make([]uint32, len(samples))
	for i, s := range samples {
		sizes[i] = uint32(len(s.Payload))
	}
	stsz, err := leafBox(mp4.BoxTypeStsz(), &mp4.Stsz{SampleSize: 0, SampleCount: uint32(len(samples)), EntrySize: sizes})
	if err != nil {
		return nil, err
	}
	stco, err := leafBox(mp4.BoxTypeStco(), &mp4.Stco{EntryCount: 1, ChunkOffset: []uint32{chunkOffset}})
	if err != nil {
		return nil, err
	}
	return containerBox(mp4.BoxTypeStbl(), stsd, stts, stsc, stsz, stco), nil
}

func boolU32(v bool) uint32 {
	if v {
		return 1
	}
	return 0
}
