package codec

import (
	"errors"
	"testing"

	"tracecheck/internal/model"
)

// buildADTS assembles a minimal valid ADTS frame with the given parameters.
func buildADTS(rateIndex, channels byte, frameLength uint16, crc bool, raw []byte) []byte {
	b := make([]byte, 7, frameLength)
	b[0] = 0xFF
	b[1] = 0xF1 // MPEG-4, layer 0, protection absent
	if crc {
		b[1] = 0xF0
		b = append(b, 0x00, 0x00) // 16-bit CRC placeholder
	}
	b[2] = (1 << 6) | (rateIndex << 2) | byte((channels>>2)&0x01) // AAC-LC profile
	b[3] = (byte(channels&0x03) << 6) | byte((frameLength>>11)&0x03)
	b[4] = byte((frameLength >> 3) & 0xFF)
	b[5] = byte((frameLength&0x07)<<5) | 0x1F
	b[6] = 0xFC
	b = append(b, raw...)
	return b[:frameLength]
}

func TestParseADTSValidFrame(t *testing.T) {
	raw := make([]byte, 20)
	frame := buildADTS(4, 2, uint16(adtsHeaderNoCRC+len(raw)), false, raw)
	res, err := ParseADTS(frame)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.SampleRate != 44100 || res.Channels != 2 || res.Samples != 1024 {
		t.Fatalf("header fields wrong: %+v", res)
	}
	// 1024 / 44100 s = 23.21995... ms, rounded half up to 23220 us.
	if res.Duration != model.Tick(23_220) {
		t.Fatalf("duration = %d, want 23220", res.Duration)
	}
}

func TestParseADTSRates(t *testing.T) {
	cases := []struct {
		index byte
		rate  uint32
	}{
		{3, 48000}, {4, 44100}, {7, 22050}, {11, 8000},
	}
	for _, tc := range cases {
		frame := buildADTS(tc.index, 1, adtsHeaderNoCRC+1, false, []byte{0xAA})
		res, err := ParseADTS(frame)
		if err != nil {
			t.Fatalf("rate index %d: %v", tc.index, err)
		}
		if res.SampleRate != tc.rate {
			t.Fatalf("rate = %d, want %d", res.SampleRate, tc.rate)
		}
	}
}

func TestParseADTSWithCRC(t *testing.T) {
	frame := buildADTS(4, 2, uint16(adtsHeaderWithCRC+5), true, make([]byte, 5))
	res, err := ParseADTS(frame)
	if err != nil {
		t.Fatalf("CRC frame: %v", err)
	}
	if res.FrameLength != adtsHeaderWithCRC+5 {
		t.Fatalf("frame length wrong: %d", res.FrameLength)
	}
}

func TestParseADTSRejectsBadInput(t *testing.T) {
	if _, err := ParseADTS(nil); !errors.Is(err, ErrTruncated) {
		t.Fatalf("nil payload: %v", err)
	}
	// Bad sync word.
	bad := buildADTS(4, 2, adtsHeaderNoCRC+1, false, []byte{1})
	bad[0] = 0x00
	if _, err := ParseADTS(bad); !errors.Is(err, ErrADTSHeader) {
		t.Fatalf("bad sync: %v", err)
	}
	// Reserved sampling index 13.
	badRate := buildADTS(13, 2, adtsHeaderNoCRC+1, false, []byte{1})
	if _, err := ParseADTS(badRate); !errors.Is(err, ErrADTSHeader) {
		t.Fatalf("bad rate index: %v", err)
	}
	// Declared frame length exceeds the actual payload.
	oversize := buildADTS(4, 2, adtsHeaderNoCRC+1, false, []byte{1})
	oversize[3] |= 0x03 // push the 13-bit frame length past the buffer
	oversize[4] = 0xFF
	oversize[5] |= 0xE0
	if _, err := ParseADTS(oversize); !errors.Is(err, ErrTruncated) {
		t.Fatalf("oversize frame: %v", err)
	}
}

func TestParseRawAAC(t *testing.T) {
	res, err := ParseRawAAC([]byte{1, 2, 3}, 48000)
	if err != nil || res.Samples != 1024 || res.Duration != model.Tick(21_333) {
		t.Fatalf("raw aac wrong: %+v %v", res, err)
	}
	// Default rate when the caller does not know it.
	res, err = ParseRawAAC([]byte{1}, 0)
	if err != nil || res.SampleRate != 44100 {
		t.Fatalf("default raw aac rate wrong: %+v %v", res, err)
	}
	if _, err := ParseRawAAC(nil, 48000); !errors.Is(err, ErrEmpty) {
		t.Fatalf("empty raw frame: %v", err)
	}
}
