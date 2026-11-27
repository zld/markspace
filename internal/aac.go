package rtpreplay

import "fmt"

// AAC sampling frequency table from ISO/IEC 14496-3, indexed by the 4-bit
// samplingFrequencyIndex carried in the AudioSpecificConfig.
var aacSampleRates = [16]uint32{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350, 0, 0, 0,
}

// AACConfig is the subset of an MPEG-4 AudioSpecificConfig needed to derive
// packet duration. Decoding the audio itself is out of scope.
type AACConfig struct {
	AudioObjectType uint8
	SampleRate      uint32
	ChannelConfig   uint8
	FrameLengthFlag bool
	DependsOnCore   bool
}

// SamplesPerFrame is 1024 for the AAC-LC default and 960 when frameLengthFlag
// is set. A frame always occupies exactly this many sample ticks.
func (c AACConfig) SamplesPerFrame() uint32 {
	if c.FrameLengthFlag {
		return 960
	}
	return 1024
}

// ParseAudioSpecificConfig parses the leading bits of an esds DecoderSpecificInfo.
func ParseAudioSpecificConfig(data []byte) (AACConfig, error) {
	if len(data) < 2 {
		return AACConfig{}, NewError(CodeAACHeader, "aac_asc", "AudioSpecificConfig shorter than 2 bytes")
	}
	br := bitReader{data: data}
	cfg := AACConfig{}
	aot, ok := br.read(5)
	if !ok {
		return cfg, NewError(CodeAACHeader, "aac_asc", "truncated audioObjectType")
	}
	if aot == 31 {
		ext, ok := br.read(6)
		if !ok {
			return cfg, NewError(CodeAACHeader, "aac_asc", "truncated extended audioObjectType")
		}
		aot = 32 + ext
	}
	if aot == 0 {
		return cfg, NewError(CodeAACHeader, "aac_asc", "audioObjectType 0 is reserved")
	}
	cfg.AudioObjectType = uint8(aot)
	idx, ok := br.read(4)
	if !ok {
		return cfg, NewError(CodeAACHeader, "aac_asc", "truncated samplingFrequencyIndex")
	}
	if idx == 15 {
		rate, ok := br.read(24)
		if !ok {
			return cfg, NewError(CodeAACHeader, "aac_asc", "truncated explicit sampling frequency")
		}
		cfg.SampleRate = uint32(rate)
	} else {
		cfg.SampleRate = aacSampleRates[idx]
	}
	if cfg.SampleRate == 0 {
		return cfg, NewError(CodeAACHeader, "aac_asc", fmt.Sprintf("invalid samplingFrequencyIndex %d", idx))
	}
	ch, ok := br.read(4)
	if !ok {
		return cfg, NewError(CodeAACHeader, "aac_asc", "truncated channelConfiguration")
	}
	cfg.ChannelConfig = uint8(ch)
	if cfg.AudioObjectType == 5 || cfg.AudioObjectType == 29 {
		// Explicit SBR/PS signalling carries the extension frequency next.
		if _, ok := br.read(4); !ok {
			return cfg, NewError(CodeAACHeader, "aac_asc", "truncated extension samplingFrequencyIndex")
		}
		core, ok := br.read(5)
		if !ok {
			return cfg, NewError(CodeAACHeader, "aac_asc", "truncated core audioObjectType")
		}
		cfg.DependsOnCore = core != 5
	} else {
		flag, ok := br.read(1)
		if !ok {
			return cfg, NewError(CodeAACHeader, "aac_asc", "truncated frameLengthFlag")
		}
		cfg.FrameLengthFlag = flag == 1
	}
	return cfg, nil
}

// AACLatmHeader is the per-frame duration carried in the RTP AAC payload when
// MPEG-4 LATM/LOAS framing is in use (RFC 3016 / RFC 3640 mpeg4-generic).
type AACLatmHeader struct {
	UseSameStream   bool
	UseSameTime     bool
	FrameLengthType uint8
	Delta           uint32 // in the stream clock, or 0 when "same time"
}

// ParseLATMHeader parses the leading LATM StreamMuxConfig flags of an RTP
// payload. It is strict: an unknown framing or a truncated header is a
// structured error, never an assumed default duration.
func ParseLATMHeader(payload []byte) (AACLatmHeader, error) {
	if len(payload) == 0 {
		return AACLatmHeader{}, NewError(CodeAACHeader, "aac_latm", "empty payload")
	}
	var h AACLatmHeader
	br := bitReader{data: payload}
	if b, ok := br.read(1); !ok || b != 0 {
		return h, NewError(CodeAACHeader, "aac_latm", "only LATM (not LOAS) payloads are supported")
	}
	useSameStream, ok := br.read(1)
	if !ok {
		return h, NewError(CodeAACHeader, "aac_latm", "truncated useSameStream bit")
	}
	h.UseSameStream = useSameStream == 1
	if !h.UseSameStream {
		streamType, ok := br.read(1)
		if !ok {
			return h, NewError(CodeAACHeader, "aac_latm", "truncated streamMuxConfig stream type")
		}
		_ = streamType
	}
	useSameTime, ok := br.read(1)
	if !ok {
		return h, NewError(CodeAACHeader, "aac_latm", "truncated useSameTime bit")
	}
	h.UseSameTime = useSameTime == 1
	if h.UseSameTime {
		// Duration identical to the previous frame; the caller must have one.
		return h, nil
	}
	if !h.UseSameStream {
		return h, NewError(CodeAACHeader, "aac_latm", "per-frame duration on a new stream is not derivable offline")
	}
	t, ok := br.read(3)
	if !ok {
		return h, NewError(CodeAACHeader, "aac_latm", "truncated frameLengthType")
	}
	h.FrameLengthType = uint8(t)
	switch t {
	case 0:
		delta, ok := br.read(8)
		if !ok {
			return h, NewError(CodeAACHeader, "aac_latm", "truncated frameLength")
		}
		h.Delta = uint32(delta)
	case 6:
		delta, ok := br.read(13)
		if !ok {
			return h, NewError(CodeAACHeader, "aac_latm", "truncated frameLength (ft=6)")
		}
		h.Delta = uint32(delta)
	default:
		return h, NewError(CodeAACHeader, "aac_latm",
			fmt.Sprintf("frameLengthType %d carries no length field and cannot be measured", t))
	}
	return h, nil
}

// bitReader is a minimal MSB-first bit reader.
type bitReader struct {
	data []byte
	pos  uint
}

func (r *bitReader) read(n uint) (uint32, bool) {
	var v uint32
	for i := uint(0); i < n; i++ {
		byteIdx := r.pos / 8
		if byteIdx >= uint(len(r.data)) {
			return 0, false
		}
		bit := (r.data[byteIdx] >> (7 - r.pos%8)) & 1
		v = v<<1 | uint32(bit)
		r.pos++
	}
	return v, true
}

// MP4Sample is the per-sample timing extracted from an MP4 audio track.
type MP4Sample struct {
	Index    int
	Size     uint32
	Duration uint32 // in track timescale units
	IsSync   bool
}

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

// OpusSampleInfo carries the dOps pre-skip, which shifts the first decodable
// sample relative to the container start.
type OpusSampleInfo struct {
	PreSkip         uint16
	InputSampleRate uint32
	Channels        uint8
}

// TotalTicks sums all sample durations in track timescale units.
func (t MP4Track) TotalTicks() (uint64, error) {
	var total uint64
	for _, s := range t.Samples {
		var err error
		total, err = addChecked(total, uint64(s.Duration), "mp4_total_duration")
		if err != nil {
			return 0, err
		}
	}
	return total, nil
}

// addChecked adds two uint64 values and reports overflow with a stable code.
func addChecked(a, b uint64, what string) (uint64, error) {
	s := a + b
	if s < a {
		return 0, NewError(CodeTimeOverflow, "add", what+": uint64 overflow")
	}
	return s, nil
}
