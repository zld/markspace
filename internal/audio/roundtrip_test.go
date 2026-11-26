package audio

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestFullNormalizeEncodeDecodeRoundTrip exercises the complete delivery path:
// decode a segment, normalize the program, encode fMP4 once, decode it back and
// confirm the delivered audio meets loudness, peak and codec-fidelity targets.
func TestFullNormalizeEncodeDecodeRoundTrip(t *testing.T) {
	if _, err := os.Stat("/usr/bin/true"); err != nil {
		t.Skip("environment check failed")
	}
	input := genADTS(t, 48000, 1, 3)
	c, err := NewConverter(TargetBitrate)
	if err != nil {
		t.Fatal(err)
	}
	seg := Segment{ID: 0, StartNanos: 0, Data: input}
	res, err := c.Process(context.Background(), seg)
	if err != nil {
		t.Fatalf("process: %v", err)
	}
	normalized, err := c.NormalizeProgram(context.Background(), res.PCM)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	out := filepath.Join(t.TempDir(), "delivered.m4s")
	if err := c.EncodeFMp4(context.Background(), normalized, out); err != nil {
		t.Fatalf("encode: %v", err)
	}
	delivered, err := c.DecodeFMp4(context.Background(), out)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(delivered) == 0 {
		t.Fatal("decoded PCM empty")
	}
	snr := SNRDelivered(normalized, delivered)
	if snr < 15.0 {
		t.Errorf("delivered SNR %.2f < 15 dB", snr)
	}
	loud, err := c.MeasureLoudness(context.Background(), delivered)
	if err != nil {
		t.Fatalf("loudness: %v", err)
	}
	if d := loud.IntegratedLUFS - TargetLUFS; d > 1.0 || d < -1.0 {
		t.Errorf("integrated LUFS %.2f off target %.1f by >1",
			loud.IntegratedLUFS, TargetLUFS)
	}
	if loud.TruePeakDBTP > TargetTP+0.5 {
		t.Errorf("true peak %.2f exceeds target %.1f",
			loud.TruePeakDBTP, TargetTP)
	}
}
