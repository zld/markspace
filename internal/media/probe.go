package media

import (
	"bytes"
	"errors"
	"io"
	"math/big"

	"github.com/abema/go-mp4"

	"archivecheck/internal/clock"
	"archivecheck/internal/diagnostic"
)

const MaxProbeBytes = 8 << 20

// Metadata is the supported MP4 projection used by policy.
type Metadata struct {
	MajorBrand string             `json:"major_brand"`
	Timescale  uint32             `json:"timescale"`
	DurationNS clock.Nanoseconds  `json:"duration_ns"`
	Tracks     []TrackDescription `json:"tracks"`
}

// ProbeMP4 reads track and media metadata with go-mp4. It does not infer
// channel layout or color conventions that are absent from the container.
func ProbeMP4(sample int, data []byte) (*Metadata, error) {
	if len(data) > MaxProbeBytes {
		return nil, diagnostic.New(diagnostic.CodeInputTooLarge, diagnostic.PhaseProbe, sample, 0,
			"MP4 input size %d exceeds limit %d", len(data), MaxProbeBytes)
	}

	info, err := mp4.Probe(bytes.NewReader(data))
	if err != nil {
		code := diagnostic.CodeInvalidMP4
		offset := int64(0)
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			code = diagnostic.CodeTruncated
			offset = int64(len(data))
		}
		return nil, diagnostic.Wrap(code, diagnostic.PhaseProbe, sample, offset, err, "MP4 probe failed")
	}
	if len(info.Tracks) == 0 {
		return nil, diagnostic.New(diagnostic.CodeInvalidMP4, diagnostic.PhaseProbe, sample, 0,
			"MP4 contains no track metadata")
	}

	audioEntries := extractAudioEntries(sample, data)
	meta := &Metadata{
		MajorBrand: string(info.MajorBrand[:]),
		Timescale:  info.Timescale,
	}
	meta.DurationNS = ticksToNanoseconds(info.Duration, info.Timescale)

	audioIndex := 0
	for i, track := range info.Tracks {
		desc := TrackDescription{
			Index:      i,
			ID:         track.TrackID,
			Timescale:  track.Timescale,
			DurationNS: int64(ticksToNanoseconds(track.Duration, track.Timescale)),
		}
		switch track.Codec {
		case mp4.CodecMP4A:
			desc.Kind = "audio"
			desc.Codec = "mp4a"
			if track.MP4A != nil {
				desc.Channels = track.MP4A.ChannelCount
			}
			if audioIndex < len(audioEntries) {
				ase := audioEntries[audioIndex]
				if desc.Channels == 0 {
					desc.Channels = ase.ChannelCount
				}
				desc.SampleRate = uint32(ase.SampleRate >> 16)
				audioIndex++
			}
		case mp4.CodecAVC1:
			desc.Kind = "video"
			desc.Codec = "avc1"
			if track.AVC != nil {
				desc.Width = uint32(track.AVC.Width)
				desc.Height = uint32(track.AVC.Height)
			}
		default:
			desc.Kind = "unknown"
			desc.Codec = "unknown"
		}
		if err := ValidateDescription(sample, desc); err != nil {
			return nil, err
		}
		meta.Tracks = append(meta.Tracks, desc)
	}
	return meta, nil
}

// AcceptGeneratedDescription is the explicit entry point for samples that
// carry a code-generated track description instead of probed box bytes.
func AcceptGeneratedDescription(sample int, desc TrackDescription) (TrackDescription, error) {
	return AcceptDescription(sample, desc)
}

func ticksToNanoseconds(ticks uint64, timescale uint32) clock.Nanoseconds {
	if timescale == 0 {
		return 0
	}
	value := new(big.Int).SetUint64(ticks)
	value.Mul(value, big.NewInt(int64(clock.Second)))
	value.Div(value, big.NewInt(int64(timescale)))
	if !value.IsInt64() {
		return 0
	}
	return clock.Nanoseconds(value.Int64())
}

func extractAudioEntries(sample int, data []byte) []*mp4.AudioSampleEntry {
	r := bytes.NewReader(data)
	path := mp4.BoxPath{
		mp4.BoxTypeMoov(), mp4.BoxTypeTrak(), mp4.BoxTypeMdia(), mp4.BoxTypeMinf(),
		mp4.BoxTypeStbl(), mp4.BoxTypeStsd(), mp4.BoxTypeMp4a(),
	}
	boxes, err := mp4.ExtractBoxesWithPayload(r, nil, []mp4.BoxPath{path})
	if err != nil {
		// Probe has already accepted the file; sample-rate extraction is an
		// enrichment and must not turn a supported track into a guess.
		return nil
	}
	_ = sample
	out := make([]*mp4.AudioSampleEntry, 0, len(boxes))
	for _, box := range boxes {
		if ase, ok := box.Payload.(*mp4.AudioSampleEntry); ok {
			out = append(out, ase)
		}
	}
	return out
}
