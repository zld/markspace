package codec

import (
	"github.com/example/mediad/internal/model"
)

// ADTS sampling_frequency_index table (ISO/IEC 13818-7 Table 35).
// Index 13-15 are reserved and must not appear in a legal frame header.
var aacSampleRates = [16]uint32{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350, 0, 0, 0,
}

// aacFrameSamples is the fixed transform block length for AAC LC/Main/SSR.
const aacFrameSamples = 1024

// AACParser parses ADTS-framed AAC packets. One packet may carry multiple
// ADTS frames; each frame contributes exactly 1024 source samples.
type AACParser struct {
	rate uint32
}

// NewAAC builds an AAC parser. The stream-advertised rate is cross-checked
// against every ADTS header's sampling_frequency_index.
func NewAAC(rate uint32) *AACParser { return &AACParser{rate: rate} }

func (p *AACParser) Name() string { return "aac" }
func (p *AACParser) Rate() uint32 { return p.rate }

// Parse walks every ADTS frame in payload, validates header fields and the
// declared frame length, and returns the exact sample span.
func (p *AACParser) Parse(seq uint16, offset int64, payload []byte) (Frame, error) {
	if len(payload) == 0 {
		return Frame{}, errAt(model.CodeTruncatedPacket, model.StageCodec, seq, offset,
			"aac: empty packet")
	}
	total := Frame{}
	pos := 0
	for pos < len(payload) {
		if len(payload)-pos < 7 {
			return Frame{}, errAt(model.CodeTruncatedPacket, model.StageCodec, seq,
				int64(offset)+int64(pos), "aac: ADTS header cut short")
		}
		b := payload[pos:]
		// 12-bit syncword 0xFFF.
		if b[0] != 0xFF || b[1]&0xF0 != 0xF0 {
			return Frame{}, errAt(model.CodeIllegalPacket, model.StageCodec, seq,
				int64(offset)+int64(pos), "aac: bad ADTS syncword")
		}
		layer := (b[1] >> 1) & 0x03
		if layer != 0 {
			return Frame{}, errAt(model.CodeIllegalPacket, model.StageCodec, seq,
				int64(offset)+int64(pos), "aac: layer field must be zero")
		}
		protectionAbsent := b[1] & 0x01
		freqIdx := (b[2] >> 2) & 0x0F
		rate := aacSampleRates[freqIdx]
		if rate == 0 {
			return Frame{}, errAt(model.CodeIllegalPacket, model.StageCodec, seq,
				int64(offset)+int64(pos), "aac: reserved sampling frequency index")
		}
		if p.rate != 0 && rate != p.rate {
			return Frame{}, errAt(model.CodeIllegalPacket, model.StageCodec, seq,
				int64(offset)+int64(pos), "aac: header rate disagrees with stream rate")
		}
		// 3-bit channel configuration spread over byte 2 (1 bit) and byte 3 (2 bits).
		chanCfg := ((b[2] & 0x01) << 2) | ((b[3] >> 6) & 0x03)
		if chanCfg == 0 {
			return Frame{}, errAt(model.CodeIllegalPacket, model.StageCodec, seq,
				int64(offset)+int64(pos), "aac: program_config_element channel layout unsupported")
		}
		// 13-bit frame_length: low 2 bits of b[3], all of b[4], high 2 bits of b[5].
		frameLen := int(b[3]&0x03)<<11 | int(b[4])<<3 | int((b[5]>>5)&0x07)
		if frameLen < 7 {
			return Frame{}, errAt(model.CodeIllegalPacket, model.StageCodec, seq,
				int64(offset)+int64(pos), "aac: frame_length smaller than header")
		}
		numBlocks := int((b[6]>>6)&0x03) + 1 // raw_data_blocks + 1 AAC frames
		hdrLen := 7
		if protectionAbsent == 0 {
			hdrLen = 9 // CRC follows the header
		}
		if frameLen < hdrLen {
			return Frame{}, errAt(model.CodeTruncatedPacket, model.StageCodec, seq,
				int64(offset)+int64(pos), "aac: frame_length cannot cover header plus CRC")
		}
		if pos+frameLen > len(payload) {
			return Frame{}, errAt(model.CodeTruncatedPacket, model.StageCodec, seq,
				int64(offset)+int64(pos), "aac: declared frame overruns packet")
		}
		total.FrameCount += numBlocks
		total.Samples += uint32(numBlocks) * aacFrameSamples
		pos += frameLen
	}
	return total, nil
}
