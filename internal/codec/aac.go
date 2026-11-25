package codec

import (
	"encoding/binary"
	"errors"
	"fmt"

	"tracecheck/internal/model"
)

// ADTS fixed header size is 7 bytes without CRC and 9 bytes with CRC.
const (
	adtsSync          = 0xFFF
	adtsHeaderNoCRC   = 7
	adtsHeaderWithCRC = 9
	aacFrameSamples   = 1024
)

// AACResult is the parsed duration of one AAC packet.
type AACResult struct {
	Profile     byte       `json:"profile"`
	SampleRate  uint32     `json:"sample_rate"`
	Channels    byte       `json:"channels"`
	FrameLength uint16     `json:"frame_length"`
	Samples     int64      `json:"samples"`
	Duration    model.Tick `json:"duration"`
}

// aacSampleRates is the ADTS sampling_frequency_index table (ISO 14496-3).
var aacSampleRates = [...]uint32{
	96000, 88200, 64000, 48000, 44100, 32000, 24000, 22050,
	16000, 12000, 11025, 8000, 7350,
}

// ErrADTSHeader is returned when the ADTS sync word or header is invalid.
var ErrADTSHeader = errors.New("invalid ADTS header")

// ParseADTS parses one ADTS frame and converts its 1024 samples to ticks
// using the rate declared in the header. It never decodes the raw AAC data.
func ParseADTS(payload []byte) (AACResult, error) {
	if len(payload) < adtsHeaderNoCRC {
		return AACResult{}, fmt.Errorf("%w: %d bytes", ErrTruncated, len(payload))
	}
	if (binary.BigEndian.Uint16(payload[0:2]) & 0xFFF6) != 0xFFF0 {
		return AACResult{}, fmt.Errorf("%w: bad sync word", ErrADTSHeader)
	}
	layer := (payload[1] >> 1) & 0x03
	if layer != 0 {
		return AACResult{}, fmt.Errorf("%w: layer must be 0", ErrADTSHeader)
	}
	protectionAbsent := payload[1] & 0x01
	profile := (payload[2] >> 6) + 1 // ADTS profile = object type - 1
	rateIndex := (payload[2] >> 2) & 0x0F
	if int(rateIndex) >= len(aacSampleRates) || aacSampleRates[rateIndex] == 0 {
		return AACResult{}, fmt.Errorf("%w: sampling index %d", ErrADTSHeader, rateIndex)
	}
	channels := ((payload[2] & 0x01) << 2) | ((payload[3] >> 6) & 0x03)
	if channels == 0 {
		return AACResult{}, fmt.Errorf("%w: zero channel count", ErrADTSHeader)
	}
	frameLength := uint16(payload[3]&0x03)<<11 |
		uint16(payload[4])<<3 |
		uint16((payload[5]>>5)&0x07)
	if frameLength < adtsHeaderNoCRC || int(frameLength) > len(payload) {
		return AACResult{}, fmt.Errorf("%w: frame_length %d exceeds payload %d",
			ErrTruncated, frameLength, len(payload))
	}
	headerLen := adtsHeaderNoCRC
	if protectionAbsent == 0 {
		headerLen = adtsHeaderWithCRC
	}
	if int(frameLength) < headerLen {
		return AACResult{}, fmt.Errorf("%w: frame too short for header", ErrTruncated)
	}

	rate := aacSampleRates[rateIndex]
	duration, err := model.TicksFromSamples(aacFrameSamples, rate)
	if err != nil {
		return AACResult{}, err
	}
	return AACResult{
		Profile:     profile,
		SampleRate:  rate,
		Channels:    channels,
		FrameLength: frameLength,
		Samples:     aacFrameSamples,
		Duration:    duration,
	}, nil
}

// ParseRawAAC handles a length-prefixed raw AAC frame (no ADTS header),
// using the caller-supplied sample rate. Duration is still exactly 1024
// samples per raw AAC frame.
func ParseRawAAC(payload []byte, sampleRate uint32) (AACResult, error) {
	if len(payload) == 0 {
		return AACResult{}, ErrTruncated
	}
	if sampleRate == 0 {
		sampleRate = 44100
	}
	duration, err := model.TicksFromSamples(aacFrameSamples, sampleRate)
	if err != nil {
		return AACResult{}, err
	}
	return AACResult{
		SampleRate:  sampleRate,
		FrameLength: uint16(len(payload)),
		Samples:     aacFrameSamples,
		Duration:    duration,
	}, nil
}

// BuildADTSForFixture assembles a minimal valid ADTS frame around raw AAC
// bytes. It is shared by the fixture generator and tests so generated
// packets always parse through the real ADTS reader.
func BuildADTSForFixture(rateIndex, channels byte, raw []byte) []byte {
	frameLength := uint16(adtsHeaderNoCRC + len(raw))
	b := make([]byte, adtsHeaderNoCRC, frameLength)
	b[0] = 0xFF
	b[1] = 0xF1 // MPEG-4, layer 0, protection absent
	b[2] = (1 << 6) | (rateIndex << 2) | byte((channels>>2)&0x01)
	b[3] = (byte(channels&0x03) << 6) | byte((frameLength>>11)&0x03)
	b[4] = byte((frameLength >> 3) & 0xFF)
	b[5] = byte((frameLength&0x07)<<5) | 0x1F
	b[6] = 0xFC
	return append(b, raw...)
}
