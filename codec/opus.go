package codec

import (
	"fmt"

	"example.com/media-kernel/media"
)

// Opus RTP timestamps and decoder output are conventionally 48 kHz.
const OpusSampleRate = 48000

// ParseOpus extracts the packet sample count from the TOC and frame-count
// fields. It does not decode audio or conceal missing frames.
func ParseOpus(packet []byte) (*Info, *media.Error) {
	const module = "codec.opus"
	if len(packet) == 0 {
		return nil, media.NewError(module, media.CodePacketEmpty, "empty Opus packet", nil)
	}
	toc := packet[0]
	config := int(toc >> 3)
	stereo := toc&0x04 != 0
	code := toc & 0x03

	frameSamples, err := opusConfigFrameSamples(config)
	if err != nil {
		return nil, media.NewError(module, media.CodeCodecInvalid, "invalid Opus TOC config", err)
	}

	frames := 1
	switch code {
	case 0:
		// One frame: the remaining bytes are self-delimiting by packet boundary.
		// A TOC-only packet can represent a zero-length/ DTX frame.
	case 1:
		// Two equal-sized frames.
		frames = 2
		body := len(packet) - 1
		if body < 2 || body%2 != 0 {
			return nil, media.NewError(module, media.CodePacketTruncated,
				fmt.Sprintf("Opus code-1 equal frames have invalid body length %d", body), nil).
				WithOffset(1)
		}
	case 2:
		// Two unequal frames have the same TOC-derived duration. The first
		// frame's self-delimiting length byte must be present.
		frames = 2
		if len(packet) < 2 {
			return nil, media.NewError(module, media.CodePacketTruncated,
				"Opus code-2 packet is missing first-frame length", nil).WithOffset(1)
		}
		firstLen, n := decodeOpusLength(packet[1:])
		if n == 0 || 1+n+int(firstLen) > len(packet) {
			return nil, media.NewError(module, media.CodePacketTruncated,
				"Opus code-2 first frame length exceeds packet boundary", nil).WithOffset(1)
		}
	case 3:
		if len(packet) < 2 {
			return nil, media.NewError(module, media.CodePacketTruncated,
				"Opus code-3 packet is missing frame-count byte", nil).WithOffset(1)
		}
		countByte := packet[1]
		frames = int(countByte & 0x3f)
		if frames == 0 || frames > 48 {
			return nil, media.NewError(module, media.CodePacketTooLong,
				fmt.Sprintf("Opus code-3 frame count %d is outside 1..48", frames), nil).WithOffset(1)
		}
	default:
		return nil, media.NewError(module, media.CodeUnsupported, fmt.Sprintf("unsupported Opus code %d", code), nil)
	}

	channels := uint8(1)
	if stereo {
		channels = 2
	}
	return &Info{
		Codec:        TypeOpus,
		Samples:      int64(frameSamples * frames),
		Frames:       frames,
		SampleRateHz: OpusSampleRate,
		Channels:     channels,
		Profile:      fmt.Sprintf("config-%d-code-%d", config, code),
	}, nil
}

func opusConfigFrameSamples(config int) (int, error) {
	if config < 0 || config > 31 {
		return 0, fmt.Errorf("config %d out of range 0..31", config)
	}
	switch {
	case config <= 11: // SILK-only: 10, 20, 40, 60 ms
		return []int{480, 960, 1920, 2880}[config%4], nil
	case config <= 15: // Hybrid: 10 or 20 ms
		return []int{480, 960}[config%2], nil
	default: // CELT-only: 2.5, 5, 10, 20 ms at 48 kHz
		return []int{120, 240, 480, 960}[config%4], nil
	}
}

// decodeOpusLength decodes the RFC 6716 self-delimiting frame length. It
// returns n=0 when the input does not contain a complete length field.
func decodeOpusLength(b []byte) (length int, n int) {
	if len(b) == 0 {
		return 0, 0
	}
	if b[0] < 252 {
		return int(b[0]), 1
	}
	if len(b) < 2 {
		return 0, 0
	}
	return 4*int(b[1]) + int(b[0]), 2
}

// BuildOpusPacket is a compact fixture helper for code-0 packets.
func BuildOpusPacket(config int, stereo bool, bodySize int) []byte {
	toc := byte((config & 31) << 3)
	if stereo {
		toc |= 0x04
	}
	pkt := make([]byte, 1+bodySize)
	pkt[0] = toc // code 0
	for i := 1; i < len(pkt); i++ {
		pkt[i] = byte(i * 17)
	}
	return pkt
}
