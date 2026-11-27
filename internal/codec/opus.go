package codec

import (
	"github.com/example/mediad/internal/model"
)

// Opus TOC configuration table. Each of the 32 configs maps to
// (numberOfStreams, mode, frameDurationMicros) per RFC 6716 section 3.1.
// Microsecond units keep CELT-only 2.5 ms frames exact under integer math.
type opusConfig struct {
	streams  int
	mode     byte // 0 SILK-only, 1 Hybrid, 2 CELT-only
	duration uint32 // microseconds
}

var opusConfigs = [32]opusConfig{
	{1, 0, 10000}, {1, 0, 20000}, {1, 0, 40000}, {1, 0, 60000},
	{2, 0, 10000}, {2, 0, 20000}, {2, 0, 40000}, {2, 0, 60000},
	{2, 0, 10000}, {2, 0, 20000}, {2, 0, 40000}, {2, 0, 60000},
	{1, 1, 10000}, {1, 1, 20000}, {2, 1, 10000}, {2, 1, 20000},
	{2, 1, 10000}, {2, 1, 20000}, {1, 2, 2500}, {1, 2, 2500},
	{1, 2, 5000}, {1, 2, 5000}, {1, 2, 7500}, {1, 2, 7500},
	{1, 2, 10000}, {1, 2, 10000}, {1, 2, 12500}, {1, 2, 12500},
	{1, 2, 20000}, {1, 2, 20000}, {2, 2, 10000}, {2, 2, 20000},
}

// OpusParser parses RFC 6716 packets clocked at a fixed source rate.
type OpusParser struct {
	rate uint32
}

// NewOpus builds an Opus parser for the given source sample rate.
func NewOpus(rate uint32) *OpusParser {
	return &OpusParser{rate: rate}
}

func (p *OpusParser) Name() string   { return "opus" }
func (p *OpusParser) Rate() uint32   { return p.rate }

// Parse validates the TOC and self-delimiting framing and returns the exact
// sample span the packet covers at the source rate.
func (p *OpusParser) Parse(seq uint16, offset int64, payload []byte) (Frame, error) {
	if len(payload) == 0 {
		return Frame{}, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
			"opus: empty packet")
	}
	toc := payload[0]
	cfg := opusConfigs[toc>>3]
	frameMicros := cfg.duration
	code := toc & 0x03

	frames, vbr, err := opusFrameLayout(seq, offset, code, payload[1:])
	if err != nil {
		return Frame{}, err
	}
	total := Frame{FrameCount: frames}
	// samples = rate * duration / 1e6, computed in 64-bit integer math.
	perFrame := uint32(uint64(p.rate) * uint64(frameMicros) / 1_000_000)
	total.Samples = perFrame * uint32(frames)
	_ = vbr
	return total, nil
}

// opusFrameLayout resolves the code byte into a frame count and verifies the
// self-delimiting length/padding framing without decoding audio content.
func opusFrameLayout(seq uint16, offset int64, code byte, body []byte) (int, bool, error) {
	switch code {
	case 0: // 1 frame in the packet
		if len(body) == 0 {
			return 0, false, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
				"opus: code 0 packet with no frame data")
		}
		return 1, false, nil
	case 1: // 2 frames, equal size, CBR
		if len(body) < 2 {
			return 0, false, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
				"opus: code 1 needs >=2 data bytes")
		}
		if len(body)%2 != 0 {
			return 0, false, errAt(model.CodeIllegalPacket, model.StageCodec, seq, offset,
				"opus: code 1 unequal frame sizes")
		}
		return 2, false, nil
	case 2: // 2 frames, VBR
		if len(body) < 2 {
			return 0, false, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
				"opus: code 2 needs length byte plus frame data")
		}
		n0 := body[0]
		if int(n0)+1 >= len(body) {
			return 0, false, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
				"opus: code 2 first frame overruns packet")
		}
		return 2, true, nil
	default: // arbitrary frame count, optional VBR/padding
		if len(body) == 0 {
			return 0, false, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
				"opus: code 3 missing frame-count byte")
		}
		m := body[0]
		vbr := m&0x80 != 0
		hasPad := m&0x40 != 0
		count := int(m&0x3f) + 1
		if count > 48 {
			return 0, false, errAt(model.CodeIllegalPacket, model.StageCodec, seq, offset,
				"opus: more than 48 frames in packet")
		}
		rest := body[1:]
		if hasPad {
			// Padding length: one or more bytes; final byte gives total.
			if len(rest) == 0 {
				return 0, false, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
					"opus: code 3 padding length missing")
			}
			padLen := 0
			i := 0
			for {
				if i >= len(rest) {
					return 0, false, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
						"opus: padding length overruns packet")
				}
				b := rest[i]
				i++
				padLen += int(b)
				if b != 255 {
					break
				}
			}
			if i+padLen > len(rest) {
				return 0, false, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
					"opus: padding overruns packet")
			}
			rest = rest[:len(rest)-padLen]
		}
		if vbr {
			// count-1 one-byte length prefixes, then count frames.
			if len(rest) < (count-1)+count {
				return 0, false, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
					"opus: code 3 VBR frames truncated")
			}
		} else if len(rest) < count {
			return 0, false, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
				"opus: code 3 CBR frames truncated")
		}
		return count, vbr, nil
	}
}
