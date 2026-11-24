package codec

import (
	"fmt"

	"example.com/media-kernel/media"
)

// AACSamplesPerFrame is the standard PCM output duration of one AAC raw block.
const AACSamplesPerFrame = 1024

var adtsSampleRates = [16]uint32{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350, 0, 0, 0,
}

// ADTSHeader contains the duration-relevant fields of one ADTS frame header.
type ADTSHeader struct {
	Info
	ProtectionAbsent bool
	HeaderLength     int
	FrameLength      int
	RawBlocks        int
}

// ParseADTSHeader parses the fixed ADTS header. It may return a non-nil header
// together with a truncation error so callers can recover the sample rate for
// an error time range.
func ParseADTSHeader(packet []byte) (*ADTSHeader, *media.Error) {
	const module = "codec.aac"
	if len(packet) < 7 {
		return nil, media.NewError(module, media.CodePacketTruncated,
			fmt.Sprintf("ADTS header needs 7 bytes, got %d", len(packet)), nil).WithOffset(0)
	}
	if packet[0] != 0xff || packet[1]&0xf0 != 0xf0 {
		return nil, media.NewError(module, media.CodeCodecInvalid, "invalid ADTS sync word", nil).WithOffset(0)
	}

	protectionAbsent := packet[1]&0x01 == 1
	headerLength := 7
	if !protectionAbsent {
		headerLength += 2
	}
	profileField := packet[2] >> 6
	rateIndex := (packet[2] >> 2) & 0x0f
	if int(rateIndex) >= len(adtsSampleRates) || adtsSampleRates[rateIndex] == 0 {
		return nil, media.NewError(module, media.CodeCodecInvalid,
			fmt.Sprintf("reserved or unsupported ADTS sample-rate index %d", rateIndex), nil).WithOffset(2)
	}
	rate := adtsSampleRates[rateIndex]
	channels := uint8(((packet[2] & 0x01) << 2) | ((packet[3] >> 6) & 0x03))
	frameLength := int((uint32(packet[3]&0x03) << 11) |
		(uint32(packet[4]) << 3) |
		(uint32(packet[5] >> 5)))
	rawBlocks := int(packet[6]&0x03) + 1

	h := &ADTSHeader{
		Info: Info{
			Codec:        TypeAAC,
			Samples:      int64(AACSamplesPerFrame * rawBlocks),
			Frames:       rawBlocks,
			SampleRateHz: rate,
			Channels:     channels,
			Profile:      fmt.Sprintf("AAC-object-%d", profileField+1),
		},
		ProtectionAbsent: protectionAbsent,
		HeaderLength:     headerLength,
		FrameLength:      frameLength,
		RawBlocks:        rawBlocks,
	}
	if len(packet) < headerLength {
		return h, media.NewError(module, media.CodePacketTruncated,
			fmt.Sprintf("ADTS CRC/header needs %d bytes, got %d", headerLength, len(packet)), nil).
			WithOffset(int64(len(packet)))
	}
	if frameLength < headerLength {
		return h, media.NewError(module, media.CodePacketTooLong,
			fmt.Sprintf("ADTS frame_length %d is shorter than header %d", frameLength, headerLength), nil).
			WithOffset(3)
	}
	return h, nil
}

// ParseAAC parses one RTP payload containing one or more ADTS frames and sums
// their output samples. Missing audio is never synthesized.
func ParseAAC(packet []byte) (*Info, *media.Error) {
	const module = "codec.aac"
	if len(packet) == 0 {
		return nil, media.NewError(module, media.CodePacketEmpty, "empty AAC packet", nil)
	}

	var (
		offset     int
		total      int64
		blocks     int
		rate       uint32
		channels   uint8
		profile    string
		adtsFrames int
	)
	for offset < len(packet) {
		h, err := ParseADTSHeader(packet[offset:])
		if err != nil {
			if h != nil && rate == 0 {
				rate = h.SampleRateHz
			}
			err.WithOffset(int64(offset) + err.Offset)
			return nil, err
		}
		if h.FrameLength > len(packet)-offset {
			e := media.NewError(module, media.CodePacketTruncated,
				fmt.Sprintf("ADTS frame declares %d bytes but only %d remain",
					h.FrameLength, len(packet)-offset), nil)
			e.WithByteRange(int64(offset), int64(len(packet)))
			return nil, e
		}
		if rate == 0 {
			rate, channels, profile = h.SampleRateHz, h.Channels, h.Profile
		} else if h.SampleRateHz != rate {
			return nil, media.NewError(module, media.CodeCodecInvalid,
				fmt.Sprintf("AAC sample rate changed from %d to %d within packet", rate, h.SampleRateHz), nil).
				WithOffset(int64(offset + 2))
		}
		total += h.Samples
		blocks += h.RawBlocks
		adtsFrames++
		offset += h.FrameLength
	}

	return &Info{
		Codec:        TypeAAC,
		Samples:      total,
		Frames:       blocks,
		SampleRateHz: rate,
		Channels:     channels,
		Profile:      fmt.Sprintf("%s;%d-adts-frames", profile, adtsFrames),
	}, nil
}

// BuildADTSFrame builds a fixture ADTS frame with a payload of payloadSize.
func BuildADTSFrame(sampleRateIndex byte, channels byte, rawBlocksMinusOne byte, payloadSize int) []byte {
	frameLength := 7 + payloadSize
	h := make([]byte, 7)
	h[0] = 0xff
	h[1] = 0xf1 // MPEG-4, layer 0, protection absent
	h[2] = (1 << 6) | ((sampleRateIndex & 0x0f) << 2) | ((channels >> 2) & 0x01)
	h[3] = ((channels & 0x03) << 6) | byte((frameLength>>11)&0x03)
	h[4] = byte((frameLength >> 3) & 0xff)
	h[5] = byte((frameLength&0x07)<<5) | 0x1f
	h[6] = 0xfc | (rawBlocksMinusOne & 0x03)
	out := append(h, make([]byte, payloadSize)...)
	for i := 7; i < len(out); i++ {
		out[i] = byte(i * 31)
	}
	return out
}
