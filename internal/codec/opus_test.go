package codec

import (
	"errors"
	"testing"

	"tracecheck/internal/model"
)

func opusTOC(config, code byte) byte { return config<<3 | code }

func TestParseOpusDurations(t *testing.T) {
	cases := []struct {
		name     string
		config   byte
		samples  int64
		duration model.Tick
	}{
		{"silk 20ms", 3, 960, 20_000},
		{"celt 2.5ms", 16, 120, 2_500},
		{"celt 5ms", 17, 240, 5_000},
		{"celt 20ms", 19, 960, 20_000},
		{"silk 60ms", 9, 2880, 60_000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := []byte{opusTOC(tc.config, 0), 0xAA}
			res, err := ParseOpus(payload)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if res.Frames != 1 || res.Samples != tc.samples {
				t.Fatalf("frames/samples = %d/%d, want 1/%d", res.Frames, res.Samples, tc.samples)
			}
			got, err := OpusDurationTicks(payload)
			if err != nil || got != tc.duration {
				t.Fatalf("duration = %d (%v), want %d", got, err, tc.duration)
			}
		})
	}
}

func TestParseOpusMultiFrameCodes(t *testing.T) {
	// Code 2: two frames without length bytes (config 3 = 20 ms SILK).
	two := []byte{opusTOC(3, 2), 0x01, 0x02}
	res, err := ParseOpus(two)
	if err != nil || res.Frames != 2 || res.Samples != 1920 {
		t.Fatalf("code 2 result wrong: %+v %v", res, err)
	}
	// Code 3: M = 3 frames, no padding.
	three := []byte{opusTOC(3, 3), 0x03, 1, 2, 3}
	res, err = ParseOpus(three)
	if err != nil || res.Frames != 3 || res.Samples != 2880 {
		t.Fatalf("code 3 result wrong: %+v %v", res, err)
	}
}

func TestParseOpusRejectsBadPackets(t *testing.T) {
	if _, err := ParseOpus(nil); !errors.Is(err, ErrTruncated) {
		t.Fatalf("empty payload: %v", err)
	}
	// Code 1 needs TOC + length + at least two frame bytes.
	if _, err := ParseOpus([]byte{opusTOC(0, 1), 0x01}); !errors.Is(err, ErrTruncated) {
		t.Fatalf("truncated code 1: %v", err)
	}
	// Code 3 needs its count byte.
	if _, err := ParseOpus([]byte{opusTOC(0, 3)}); !errors.Is(err, ErrTruncated) {
		t.Fatalf("missing code 3 count: %v", err)
	}
	// Zero frames is illegal framing.
	if _, err := ParseOpus([]byte{opusTOC(0, 3), 0x00}); !errors.Is(err, ErrUndecidable) {
		t.Fatalf("zero frame count: %v", err)
	}
	// Code 3 declaring more frames than bytes available.
	if _, err := ParseOpus([]byte{opusTOC(0, 3), 0x05, 1, 2}); !errors.Is(err, ErrTruncated) {
		t.Fatalf("declared frames beyond payload: %v", err)
	}
}

func TestParseOpusAllConfigsHaveKnownSamples(t *testing.T) {
	for config := 0; config < 32; config++ {
		res, err := ParseOpus([]byte{opusTOC(byte(config), 0), 0x00})
		if err != nil {
			t.Fatalf("config %d: %v", config, err)
		}
		if res.Samples <= 0 || res.FrameTicks <= 0 {
			t.Fatalf("config %d produced no duration: %+v", config, res)
		}
	}
}
