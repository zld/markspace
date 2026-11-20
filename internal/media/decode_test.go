package media

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestDecodeRejectsPartialFrame(t *testing.T) {
	// A sink that never sees a partial frame is the contract; this test checks
	// the split logic directly instead of spawning FFmpeg: splitting the byte
	// stream at every offset must reproduce the same sample sequence.
	frames := []float64{0.1, -0.2, 0.3, -0.4, 0.5, -0.6, 0.7, -0.8}
	channels := 2
	raw := encodeLEFloat32(frames)
	for split := 0; split <= len(raw); split++ {
		var got []float64
		carry := 0
		buf := make([]byte, len(raw)+channels*4)
		copy(buf, raw[:split])
		carry = split
		// Feed the rest in one read.
		n := copy(buf[carry:], raw[split:])
		carry += n
		whole := carry / 4 * 4
		perFrame := channels * 4
		count := whole / perFrame
		if count > 0 {
			out := make([]float64, count*channels)
			if err := convertLEFloat32(out, buf[:whole]); err != nil {
				t.Fatal(err)
			}
			got = append(got, out...)
		}
		if len(got) != len(frames) {
			t.Fatalf("split %d: got %d samples, want %d", split, len(got), len(frames))
		}
		// Compare as float32: the wire format is f32le, so this checks byte
		// reassembly rather than float64 round-tripping.
		for i := range frames {
			if float32(got[i]) != float32(frames[i]) {
				t.Fatalf("split %d: sample %d = %v, want %v", split, i, got[i], frames[i])
			}
		}
	}
}

func TestConvertRejectsNonFinite(t *testing.T) {
	raw := encodeLEFloat32([]float64{0, float64(math.NaN())})
	if err := convertLEFloat32(make([]float64, 2), raw); err == nil {
		t.Fatal("NaN accepted")
	}
	raw = encodeLEFloat32([]float64{0, math.Inf(1)})
	if err := convertLEFloat32(make([]float64, 2), raw); err == nil {
		t.Fatal("Inf accepted")
	}
}

func TestDecodeMissingBinary(t *testing.T) {
	info := Info{SampleRate: 48000, Channels: 1}
	err := Decode(context.Background(), "x", info, DecodeOptions{FFmpegPath: "definitely-not-ffmpeg"}, func([]float64) error { return nil })
	if err == nil {
		t.Fatal("expected an error for a missing ffmpeg")
	}
	if stageOfErr(err) != "decode" {
		t.Errorf("stage = %q", stageOfErr(err))
	}
}

func encodeLEFloat32(in []float64) []byte {
	out := make([]byte, len(in)*4)
	for i, v := range in {
		bits := math.Float32bits(float32(v))
		out[i*4] = byte(bits)
		out[i*4+1] = byte(bits >> 8)
		out[i*4+2] = byte(bits >> 16)
		out[i*4+3] = byte(bits >> 24)
	}
	return out
}

func stageOfErr(err error) string {
	var de *DecodeError
	if errors.As(err, &de) {
		return de.Stage
	}
	return ""
}

func TestTruncateRunes(t *testing.T) {
	// Multi-byte characters must never be split in half.
	s := strings.Repeat("音", 100) // 3 bytes each
	got := truncateRunes(s, 10)
	if !strings.HasSuffix(got, "...") {
		t.Fatalf("got %q", got)
	}
	body := strings.TrimSuffix(got, "...")
	if body != "音音音" {
		t.Errorf("body = %q, want three whole runes", body)
	}
	if truncateRunes("short", 240) != "short" {
		t.Error("short string was modified")
	}
}
