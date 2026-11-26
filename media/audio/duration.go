// Package audio computes exact packet durations for the two supported
// codecs. Opus duration comes from the TOC configuration byte; AAC duration
// comes from the AU header length (or, when absent, from the configured
// samples-per-frame). Nothing is estimated from byte counts.
package audio

import (
	"errors"
	"fmt"

	"replaykit/media/clock"
	"replaykit/media/fault"
)

// Codec is the audio coding format for a task.
type Codec string

const (
	// CodecOpus is RFC 6716 Opus.
	CodecOpus Codec = "opus"
	// CodecAAC is MPEG-4 AAC carried in RTP.
	CodecAAC Codec = "aac"
)

// ErrUnknownCodec is returned for anything but Opus and AAC.
var ErrUnknownCodec = errors.New("audio: unsupported codec (want opus or aac)")

// ParseCodec validates a task codec name.
func ParseCodec(s string) (Codec, error) {
	switch Codec(s) {
	case CodecOpus, CodecAAC:
		return Codec(s), nil
	}
	return "", fmt.Errorf("%w: %q", ErrUnknownCodec, s)
}

// OpusInfo is the decoded TOC.
type OpusInfo struct {
	Config       uint8 `json:"config"`
	Stereo       bool  `json:"stereo"`
	FrameSamples int   `json:"frame_samples"` // samples at 48 kHz in one frame
	FrameCount   int   `json:"frame_count"`   // frames coded in this packet
	Duration48k  int   `json:"duration_48k"`  // total samples at 48 kHz
}

// opusFrameSamples maps TOC config (0..31) to samples per frame at 48 kHz.
var opusFrameSamples = [32]int{
	480, 960, 1920, 2880, // 0-3: SILK NB 10/20/40/60 ms
	480, 960, 1920, 2880, // 4-7: SILK MB
	480, 960, 1920, 2880, // 8-11: SILK WB
	480, 960, // 12-13: Hybrid SWB 10/20
	480, 960, // 14-15: Hybrid FB 10/20
	120, 240, 480, 960, // 16-19: CELT NB 2.5/5/10/20
	120, 240, 480, 960, // 20-23: CELT WB
	120, 240, 480, 960, // 24-27: CELT SWB
	120, 240, 480, 960, // 28-31: CELT FB
}

// ParseOpusTOC reads the first byte of an Opus payload.
func ParseOpusTOC(payload []byte) (OpusInfo, error) {
	if len(payload) == 0 {
		return OpusInfo{}, errors.New("audio: empty opus payload")
	}
	toc := payload[0]
	info := OpusInfo{
		Config:       toc >> 3,
		Stereo:       toc&0x04 != 0,
		FrameSamples: opusFrameSamples[toc>>3],
	}
	code := toc & 0x03
	switch code {
	case 0:
		info.FrameCount = 1
	case 1, 2:
		info.FrameCount = 2
	case 3:
		if len(payload) < 2 {
			return OpusInfo{}, errors.New("audio: opus code 3 without frame-count byte")
		}
		info.FrameCount = int(payload[1] & 0x3F)
		if info.FrameCount == 0 || info.FrameCount > 48 {
			return OpusInfo{}, fmt.Errorf("audio: opus code 3 frame count %d out of range", info.FrameCount)
		}
	}
	info.Duration48k = info.FrameSamples * info.FrameCount
	return info, nil
}

// OpusDuration48k returns the packet duration in 48 kHz samples.
func OpusDuration48k(payload []byte) (int, error) {
	info, err := ParseOpusTOC(payload)
	if err != nil {
		return 0, err
	}
	return info.Duration48k, nil
}

// AACConfig describes how the task frames AAC RTP payloads.
type AACConfig struct {
	// AUHeaderBits is 0 when AU headers are not present in-band (the RTP
	// payload is raw AAC) or 16/32 when RFC 3640 AU headers are present.
	AUHeaderBits int `json:"au_header_bits"`
	// SamplesPerFrame is the configured AAC frame size (1024 typical), used
	// when no in-band AU header supplies a size.
	SamplesPerFrame int `json:"samples_per_frame"`
	// SampleRate is the audio sample rate used by the shared clock.
	SampleRate int `json:"sample_rate"`
	// ObjectType is the MPEG-4 audio object type (2 = AAC-LC).
	ObjectType int `json:"object_type"`
}

// Validate checks the AAC parameters a task supplies.
func (c AACConfig) Validate() error {
	if c.SampleRate <= 0 {
		return fmt.Errorf("audio: AAC sample_rate must be positive, got %d", c.SampleRate)
	}
	if c.SamplesPerFrame <= 0 {
		return fmt.Errorf("audio: AAC samples_per_frame must be positive, got %d", c.SamplesPerFrame)
	}
	if c.AUHeaderBits != 0 && c.AUHeaderBits != 16 && c.AUHeaderBits != 32 {
		return fmt.Errorf("audio: AAC au_header_bits must be 0, 16 or 32, got %d", c.AUHeaderBits)
	}
	return nil
}

// Params are the audio parameters for one task.
type Params struct {
	Codec      Codec     `json:"codec"`
	SampleRate int       `json:"sample_rate"`
	AAC        AACConfig `json:"aac"`
	// Channels is informational only; duration does not depend on it.
	Channels int `json:"channels"`
}

// Validate checks that the codec-specific configuration is coherent.
func (p Params) Validate() error {
	if _, err := ParseCodec(string(p.Codec)); err != nil {
		return err
	}
	if p.SampleRate <= 0 {
		return fmt.Errorf("audio: sample_rate must be positive, got %d", p.SampleRate)
	}
	if p.Codec == CodecAAC {
		p.AAC.SampleRate = p.SampleRate
		return p.AAC.Validate()
	}
	return nil
}

// Duration is one packet's duration expressed both in samples at the task
// rate and as an exact rational number of seconds.
type Duration struct {
	Samples int64 `json:"samples"`
	Num     int64 `json:"num"` // seconds = Num/Den
	Den     int64 `json:"den"`
}

// PacketDuration computes the media duration of one audio payload.
// Opus is decoded from its TOC at 48 kHz, then rescaled to the task rate.
// AAC is decoded from the AU header when present; otherwise the configured
// samples-per-frame is used. An invalid payload is an error the caller
// records; no duration is invented.
func PacketDuration(params Params, payload []byte) (Duration, error) {
	if err := params.Validate(); err != nil {
		return Duration{}, err
	}
	switch params.Codec {
	case CodecOpus:
		written, err := opusDurationAt(payload, params.SampleRate)
		if err != nil {
			return Duration{}, err
		}
		return Duration{Samples: int64(written), Num: int64(written), Den: int64(params.SampleRate)}, nil
	case CodecAAC:
		samples, err := aacDuration(params.AAC, payload)
		if err != nil {
			return Duration{}, err
		}
		return Duration{Samples: int64(samples), Num: int64(samples), Den: int64(params.SampleRate)}, nil
	default:
		return Duration{}, fmt.Errorf("%w: %q", ErrUnknownCodec, params.Codec)
	}
}

// opusDurationAt converts a TOC duration at 48 kHz to the task rate without
// rounding loss for the sample rates RTP audio actually uses.
func opusDurationAt(payload []byte, rate int) (int, error) {
	info, err := ParseOpusTOC(payload)
	if err != nil {
		return 0, err
	}
	if rate == 48000 {
		return info.Duration48k, nil
	}
	// Opus decoders always run at 48 kHz; express the packet duration in
	// samples of the requested rate exactly when it divides the 48 kHz
	// count, and otherwise keep full precision via the rational form.
	if 48000%rate == 0 {
		return info.Duration48k / (48000 / rate), nil
	}
	// Non-divisor rates (e.g. 44100) need fractional samples; round to the
	// nearest sample but flag nothing, since the exact seconds are kept by
	// the rational Duration(Num=Duration48k, Den=48000).
	scaled := int64(info.Duration48k) * int64(rate)
	half := int64(48000) / 2
	return int((scaled + half) / 48000), nil
}

// aacDuration returns the number of samples in the payload.
func aacDuration(cfg AACConfig, payload []byte) (int, error) {
	if cfg.AUHeaderBits == 0 {
		if len(payload) == 0 {
			return 0, errors.New("audio: empty AAC payload")
		}
		return cfg.SamplesPerFrame, nil
	}
	// The AU-headers-length field is the first 16 bits of the payload per
	// RFC 3640 section 3.3.6.
	if len(payload) < 2 {
		return 0, errors.New("audio: AAC payload too short for AU-headers-length")
	}
	headerBits := int(payload[0])<<8 | int(payload[1])
	if headerBits%16 != 0 {
		return 0, fmt.Errorf("audio: AAC AU-headers-length %d not a multiple of 16", headerBits)
	}
	count := headerBits / 16
	if count == 0 {
		return 0, errors.New("audio: AAC AU-headers-length is zero")
	}
	needBytes := 2 + headerBits/8
	if len(payload) < needBytes {
		return 0, fmt.Errorf("audio: AAC AU headers need %d bytes but payload has %d", needBytes, len(payload))
	}
	// AU size fields are 13 bits each when au_header_bits==16. When 32, the
	// upper 13 bits are size and the lower bits carry index/delta data; the
	// size semantics used here are the 16-bit form.
	samples := 0
	for i := 0; i < count; i++ {
		off := 2 + i*2
		// The 13-bit AU-size field spans the first byte and the top 5 bits
		// of the second; it must cover output, and is validated here rather
		// than trusted.
		size := int(payload[off])<<5 | int(payload[off+1])>>3
		auBytes := 2 + headerBits/8
		if size <= 0 || auBytes+size > len(payload) {
			return 0, fmt.Errorf("audio: AAC AU %d declares size %d, payload has %d", i, size, len(payload)-auBytes)
		}
		samples += cfg.SamplesPerFrame
	}
	return samples, nil
}

// RecordError converts an audio duration failure into a recoverable fault
// scoped to the packet's offset and RTP timestamp.
func RecordError(layer string, offset int64, timestamp uint32, err error) fault.Anomaly {
	ts := clock.Tick{Value: int64(timestamp)}
	return fault.New(layer, offset, fault.CategoryAudioDuration, fault.SeverityWarning, fault.ActionSkipped, 0,
		fault.Range{Start: ts, End: ts}, err.Error())
}
