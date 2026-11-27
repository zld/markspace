package rtpreplay

import (
	"bytes"

	"github.com/abema/go-mp4"
)

// MP4Track holds the timing facts needed to compare a container timeline with
// an RTP-derived one.
type MP4Track struct {
	TrackID   uint32
	Codec     Codec
	Timescale uint32
	Samples   []MP4Sample
	AAC       *AACConfig
	Opus      *OpusSampleInfo
}

// readAudioTrack collects the boxes of one trak while walking the box tree.
type readAudioTrack struct {
	trackID   uint32
	timescale uint32
	codec     Codec
	hasSample bool
	stts      *mp4.Stts
	stsz      *mp4.Stsz
	dops      *mp4.DOps
	asc       []byte
}

// ReadMP4Track extracts the first supported audio track.
func ReadMP4Track(data []byte) (*MP4Track, error) {
	tracks, err := ReadMP4Tracks(data)
	if err != nil {
		return nil, err
	}
	if len(tracks) == 0 {
		return nil, NewError(CodeMP4, "mp4_probe", "no supported audio track found")
	}
	return tracks[0], nil
}

// ReadMP4Tracks extracts every supported audio track, in file order. AAC
// tracks carry an AudioSpecificConfig from esds; Opus tracks carry dOps
// pre-skip. Each track's own mdhd timescale is preserved so the caller can
// convert durations into the RTP clock with exact integer arithmetic.
func ReadMP4Tracks(data []byte) ([]*MP4Track, error) {
	var traks []*readAudioTrack
	var cur *readAudioTrack

	handler := func(h *mp4.ReadHandle) (interface{}, error) {
		// A trak node starts a new per-track accumulator; its children still
		// need to be expanded, so fall through to Expand below.
		if h.BoxInfo.Type == mp4.BoxTypeTrak() {
			cur = &readAudioTrack{}
			traks = append(traks, cur)
		} else if cur != nil {
			switch h.BoxInfo.Type {
			case mp4.BoxTypeTkhd(), mp4.BoxTypeMdhd(), mp4.BoxTypeStts(), mp4.BoxTypeStsz(),
				mp4.BoxTypeDOps(), mp4.BoxTypeEsds():
				box, _, err := h.ReadPayload()
				if err != nil {
					return nil, NewError(CodeMP4, "mp4_read", err.Error()).WithCause(err)
				}
				switch b := box.(type) {
				case *mp4.Tkhd:
					cur.trackID = b.TrackID
				case *mp4.Mdhd:
					cur.timescale = b.Timescale
				case *mp4.Stts:
					cur.stts = b
				case *mp4.Stsz:
					cur.stsz = b
				case *mp4.DOps:
					cur.dops = b
					cur.codec = CodecOpus
					cur.hasSample = true
				case *mp4.Esds:
					cur.asc = findDecSpecificInfo(b.Descriptors)
					cur.codec = CodecAAC
					cur.hasSample = true
				}
			}
		}
		_, err := h.Expand()
		return nil, err
	}
	if _, err := mp4.ReadBoxStructure(bytes.NewReader(data), handler); err != nil {
		return nil, NewError(CodeMP4, "mp4_probe", err.Error()).WithCause(err)
	}
	var out []*MP4Track
	for _, t := range traks {
		if !t.hasSample {
			continue
		}
		tr, err := buildMP4Track(t)
		if err != nil {
			return nil, err
		}
		out = append(out, tr)
	}
	return out, nil
}

func buildMP4Track(t *readAudioTrack) (*MP4Track, error) {
	if t.timescale == 0 {
		return nil, NewError(CodeMP4, "mp4_track", "track timescale is zero")
	}
	if t.stts == nil {
		return nil, NewError(CodeMP4, "mp4_track", "stts box not found")
	}
	out := &MP4Track{TrackID: t.trackID, Codec: t.codec, Timescale: t.timescale}
	uniform := uint32(0)
	if t.stsz != nil {
		uniform = t.stsz.SampleSize
	}
	idx := 0
	for _, e := range t.stts.Entries {
		for i := uint32(0); i < e.SampleCount; i++ {
			size := uniform
			if t.stsz != nil && t.stsz.SampleSize == 0 && idx < len(t.stsz.EntrySize) {
				size = t.stsz.EntrySize[idx]
			}
			out.Samples = append(out.Samples, MP4Sample{Index: idx, Size: size, Duration: e.SampleDelta, IsSync: true})
			idx++
		}
	}
	switch t.codec {
	case CodecAAC:
		if len(t.asc) == 0 {
			return nil, NewError(CodeMP4, "mp4_esds", "DecoderSpecificInfo descriptor not found")
		}
		cfg, err := ParseAudioSpecificConfig(t.asc)
		if err != nil {
			return nil, AsError(err)
		}
		if cfg.SampleRate == 0 {
			return nil, NewError(CodeMP4, "mp4_esds", "AudioSpecificConfig has no sample rate")
		}
		out.AAC = &cfg
	case CodecOpus:
		if t.dops != nil {
			out.Opus = &OpusSampleInfo{
				PreSkip: t.dops.PreSkip, InputSampleRate: t.dops.InputSampleRate,
				Channels: t.dops.OutputChannelCount,
			}
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
