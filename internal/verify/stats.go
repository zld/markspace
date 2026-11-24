package verify

import (
	"math"

	"media-archiver/internal/media"
)

// Stats contains bitrate and PCM quality measurements.
type Stats struct {
	Frames              int64   `json:"frames"`
	Channels            int     `json:"channels"`
	SampleRate          int     `json:"sample_rate"`
	BitsPerSample       int     `json:"bits_per_sample"`
	PCMBytes            int     `json:"pcm_bytes"`
	TargetBitrateBPS    int64   `json:"target_bitrate_bps"`
	ActualBitrateBPS    float64 `json:"actual_bitrate_bps"`
	BitrateDeviationPct float64 `json:"bitrate_deviation_pct"`
	Peak                int64   `json:"peak"`
	PeakNormalized      float64 `json:"peak_normalized"`
	ClippedSamples      int64   `json:"clipped_samples"`
	BadBlocks           int     `json:"bad_blocks"`
	DurationNS          int64   `json:"duration_ns"`
}

// Compute decodes integer PCM and calculates bitrate and clipping statistics.
func Compute(format media.PCMFormat, pcm []byte, badBlocks int) (Stats, error) {
	if err := format.Validate(); err != nil {
		return Stats{}, err
	}
	bytesPerSample := format.SampleByteCount()
	frameBytes := format.FrameByteCount()
	if len(pcm)%frameBytes != 0 {
		return Stats{}, media.NewError(media.CodeValidation, media.StageVerify, -1, "PCM is not frame aligned", nil)
	}
	frames := int64(len(pcm) / frameBytes)
	tb := media.Timebase{SampleRate: format.Rate()}
	target := int64(format.SampleRate) * int64(format.Channels) * int64(format.BitsPerSample)
	durationNS := tb.FramesToNS(frames)

	var actual float64
	if frames > 0 {
		actual = float64(len(pcm)*8) * float64(format.SampleRate) / float64(frames)
	}
	deviation := 0.0
	if target != 0 {
		deviation = (actual - float64(target)) / float64(target) * 100
	}

	s := Stats{
		Frames:              frames,
		Channels:            format.ChannelCount(),
		SampleRate:          format.Rate(),
		BitsPerSample:       format.BitDepth(),
		PCMBytes:            len(pcm),
		TargetBitrateBPS:    target,
		ActualBitrateBPS:    math.Round(actual*1000) / 1000,
		BitrateDeviationPct: math.Round(deviation*100000) / 100000,
		DurationNS:          durationNS,
		BadBlocks:           badBlocks,
	}
	if err := s.scanSamples(pcm, bytesPerSample); err != nil {
		return Stats{}, err
	}
	return s, nil
}

func (s *Stats) scanSamples(pcm []byte, bytesPerSample int) error {
	maxPositive := int64(1)<<(s.BitsPerSample-1) - 1
	minNegative := -int64(1) << (s.BitsPerSample - 1)
	for off := 0; off+bytesPerSample <= len(pcm); off += bytesPerSample {
		v, clippedRaw, err := decodeIntegerSample(pcm[off:off+bytesPerSample], s.BitsPerSample)
		if err != nil {
			return media.NewError(media.CodeValidation, media.StageVerify, int64(off), "decode PCM sample", err)
		}
		abs := v
		if abs < 0 {
			abs = -abs
		}
		if abs > s.Peak {
			s.Peak = abs
		}
		denom := float64(maxPositive)
		if v == minNegative {
			denom = float64(-minNegative)
		}
		if n := math.Abs(float64(v)) / denom; n > s.PeakNormalized {
			s.PeakNormalized = n
		}
		if clippedRaw || v == maxPositive || v == minNegative {
			s.ClippedSamples++
		}
	}
	s.PeakNormalized = math.Round(s.PeakNormalized*1e6) / 1e6
	return nil
}

func decodeIntegerSample(b []byte, bits int) (int64, bool, error) {
	switch bits {
	case 8:
		// WAV 8-bit PCM is unsigned.
		raw := b[0]
		return int64(int(raw) - 128), raw == 0 || raw == 255, nil
	case 16:
		v := int64(uint16(b[0]) | uint16(b[1])<<8)
		if v&0x8000 != 0 {
			v -= 1 << 16
		}
		return v, false, nil
	case 24:
		v := int64(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16)
		if v&0x800000 != 0 {
			v -= 1 << 24
		}
		return v, false, nil
	case 32:
		v := int64(uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24)
		if v&0x80000000 != 0 {
			v -= 1 << 32
		}
		return v, false, nil
	default:
		return 0, false, media.NewError(media.CodeUnsupported, media.StageVerify, -1, "unsupported decode bit depth", nil)
	}
}

// BitrateWithinTolerance reports whether absolute deviation is within percent.
func (s Stats) BitrateWithinTolerance(percent float64) bool {
	return math.Abs(s.BitrateDeviationPct) <= percent
}
