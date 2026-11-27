package rtpreplay

import (
	"bytes"

	"github.com/abema/go-mp4"
)

// ReadMP4Track opens an in-memory MP4 and extracts the first audio track's
// sample durations and codec configuration using the go-mp4 box parser. The
// track timescale is preserved so the caller can convert to the stream clock
// with exact integer arithmetic.
func ReadMP4Track(data []byte) (*MP4Track, error) {
	r := bytes.NewReader(data)
	info, err := mp4.Probe(r)
	if err != nil {
		return nil, NewError(CodeMP4, "mp4_probe", err.Error()).WithCause(err)
	}
	if len(info.Tracks) == 0 {
		return nil, NewError(CodeMP4, "mp4_probe", "no tracks found")
	}
	for _, t := range info.Tracks {
		if t.Codec == mp4.CodecMP4A {
			return readAudioTrack(r, t, info.Tracks)
		}
	}
	// Opus-in-MP4 tracks are not classified by go-mp4's Probe; locate them by
	// sample entry type.
	track, err := readOpusTrack(r)
	if err != nil {
		return nil, err
	}
	if track != nil {
		return track, nil
	}
	return nil, NewError(CodeMP4, "mp4_probe", "no supported audio track found")
}

func readAudioTrack(r *bytes.Reader, t *mp4.Track, all mp4.Tracks) (*MP4Track, error) {
	if t.Timescale == 0 {
		return nil, NewError(CodeMP4, "mp4_track", "track timescale is zero")
	}
	out := &MP4Track{TrackID: t.TrackID, Codec: CodecAAC, Timescale: t.Timescale}
	uniform, err := readUniformSampleSize(r)
	if err != nil {
		return nil, err
	}
	for i, s := range t.Samples {
		size := s.Size
		if size == 0 {
			// go-mp4's Probe only fills EntrySize; a uniform stsz stores a
			// single shared size instead.
			size = uniform
		}
		out.Samples = append(out.Samples, MP4Sample{Index: i, Size: size, Duration: s.TimeDelta, IsSync: true})
	}
	cfg, err := readASCFromTrack(r, t.TrackID, all)
	if err != nil {
		return nil, err
	}
	out.AAC = cfg
	return out, nil
}

func readASCFromTrack(r *bytes.Reader, trackID uint32, all mp4.Tracks) (*AACConfig, error) {
	_ = trackID
	_ = all
	// Re-scan for the esds of this track to obtain DecoderSpecificInfo.
	boxes, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeMp4a(), mp4.BoxTypeEsds()},
	})
	if err != nil {
		return nil, NewError(CodeMP4, "mp4_esds", err.Error()).WithCause(err)
	}
	if len(boxes) == 0 {
		return nil, NewError(CodeMP4, "mp4_esds", "esds box not found for AAC track")
	}
	esds, ok := boxes[0].Payload.(*mp4.Esds)
	if !ok {
		return nil, NewError(CodeMP4, "mp4_esds", "esds payload has unexpected type")
	}
	specific := findDecSpecificInfo(esds.Descriptors)
	if len(specific) == 0 {
		return nil, NewError(CodeMP4, "mp4_esds", "DecoderSpecificInfo descriptor not found")
	}
	cfg, err := ParseAudioSpecificConfig(specific)
	if err != nil {
		return nil, AsError(err)
	}
	if cfg.SampleRate == 0 {
		return nil, NewError(CodeMP4, "mp4_esds", "AudioSpecificConfig has no sample rate")
	}
	return &cfg, nil
}

// readUniformSampleSize returns stsz.SampleSize when the table uses the
// uniform form, or 0 when per-sample sizes are present.
func readUniformSampleSize(r *bytes.Reader) (uint32, error) {
	boxes, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
	})
	if err != nil {
		return 0, NewError(CodeMP4, "mp4_stsz", err.Error()).WithCause(err)
	}
	for _, b := range boxes {
		if stsz, ok := b.Payload.(*mp4.Stsz); ok {
			return stsz.SampleSize, nil
		}
	}
	return 0, nil
}

// readOpusTrack finds an Opus sample entry and derives sample durations from
// the stts table with the track timescale.
func readOpusTrack(r *bytes.Reader) (*MP4Track, error) {
	payloads, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeTkhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMdhd()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeOpus()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeOpus(), mp4.BoxTypeDOps()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStts()},
		{mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
			mp4.BoxTypeStbl(), mp4.BoxTypeStsz()},
	})
	if err != nil {
		return nil, NewError(CodeMP4, "mp4_opus", err.Error()).WithCause(err)
	}
	if len(payloads) == 0 {
		return nil, nil
	}
	var tkhd *mp4.Tkhd
	var mdhd *mp4.Mdhd
	var dops *mp4.DOps
	var stts *mp4.Stts
	var stsz *mp4.Stsz
	for _, p := range payloads {
		switch v := p.Payload.(type) {
		case *mp4.Tkhd:
			tkhd = v
		case *mp4.Mdhd:
			mdhd = v
		case *mp4.DOps:
			dops = v
		case *mp4.Stts:
			stts = v
		case *mp4.Stsz:
			stsz = v
		}
	}
	if tkhd == nil || mdhd == nil || stts == nil {
		return nil, NewError(CodeMP4, "mp4_opus", "missing tkhd/mdhd/stts for Opus track")
	}
	if mdhd.Timescale == 0 {
		return nil, NewError(CodeMP4, "mp4_opus", "Opus track timescale is zero")
	}
	out := &MP4Track{TrackID: tkhd.TrackID, Codec: CodecOpus, Timescale: mdhd.Timescale}
	idx := 0
	for _, e := range stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			size := uint32(0)
			if stsz != nil {
				if stsz.SampleSize != 0 {
					size = stsz.SampleSize
				} else if idx < len(stsz.EntrySize) {
					size = stsz.EntrySize[idx]
				}
			}
			out.Samples = append(out.Samples, MP4Sample{Index: idx, Size: size, Duration: e.SampleDelta, IsSync: true})
			idx++
		}
	}
	if dops != nil {
		out.Opus = &OpusSampleInfo{
			PreSkip: dops.PreSkip, InputSampleRate: dops.InputSampleRate,
			Channels: dops.OutputChannelCount,
		}
	}
	return out, nil
}

// findDecSpecificInfo returns the DecoderSpecificInfo payload. go-mp4 parses
// esds descriptors as a flat list, so a tag scan is sufficient.
func findDecSpecificInfo(descs []mp4.Descriptor) []byte {
	for i := range descs {
		if descs[i].Tag == 0x05 {
			return descs[i].Data
		}
	}
	return nil
}
