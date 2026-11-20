package media

import (
	"context"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// writeFloatWAV writes a minimal float32 WAV with the given interleaved
// samples. It is intentionally simple: the fixture package covers the
// extensible format, this helper only needs something FFmpeg can decode.
func writeFloatWAV(t *testing.T, path string, channels, rate int, samples []float64) {
	t.Helper()
	data := make([]byte, len(samples)*4)
	for i, v := range samples {
		binary.LittleEndian.PutUint32(data[i*4:], math.Float32bits(float32(v)))
	}
	var h []byte
	h = append(h, "RIFF"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(36+len(data)))
	h = append(h, "WAVEfmt "...)
	h = binary.LittleEndian.AppendUint32(h, 16)
	h = binary.LittleEndian.AppendUint16(h, 3) // IEEE float
	h = binary.LittleEndian.AppendUint16(h, uint16(channels))
	h = binary.LittleEndian.AppendUint32(h, uint32(rate))
	h = binary.LittleEndian.AppendUint32(h, uint32(rate*channels*4))
	h = binary.LittleEndian.AppendUint16(h, uint16(channels*4))
	h = binary.LittleEndian.AppendUint16(h, 32)
	h = append(h, "data"...)
	h = binary.LittleEndian.AppendUint32(h, uint32(len(data)))
	h = append(h, data...)
	if err := os.WriteFile(path, h, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestDecodeStereoInterleave checks the real FFmpeg path: channel values must
// arrive in order with no drift from frame carry across reads.
func TestDecodeStereoInterleave(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	if runtime.GOOS == "windows" {
		t.Skip("wav fixture uses POSIX paths")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "interleave.wav")
	const (
		channels = 2
		rate     = 48000
		frames   = 10007 // deliberately not a multiple of the chunk size
	)
	samples := make([]float64, frames*channels)
	for i := 0; i < frames; i++ {
		samples[i*channels] = 0.25
		samples[i*channels+1] = -0.5
	}
	writeFloatWAV(t, path, channels, rate, samples)

	info, err := Probe(context.Background(), path, Options{})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	var got []float64
	err = Decode(context.Background(), path, info, DecodeOptions{ChunkFrames: 1000}, func(chunk []float64) error {
		if len(chunk)%info.Channels != 0 {
			t.Errorf("partial frame handed to sink: %d samples", len(chunk))
		}
		got = append(got, chunk...)
		return nil
	})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != frames*channels {
		t.Fatalf("got %d samples, want %d", len(got), frames*channels)
	}
	for i := 0; i < frames; i++ {
		if math.Abs(got[i*channels]-0.25) > 1e-6 {
			t.Fatalf("frame %d left = %v, want 0.25", i, got[i*channels])
		}
		if math.Abs(got[i*channels+1]+0.5) > 1e-6 {
			t.Fatalf("frame %d right = %v, want -0.5", i, got[i*channels+1])
		}
	}
}

func TestDecodeHonoursDurationLimit(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not installed")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "long.wav")
	const rate = 48000
	writeFloatWAV(t, path, 1, rate, make([]float64, rate)) // 1 s
	info := Info{SampleRate: rate, Channels: 1, Duration: 1}
	err := Decode(context.Background(), path, info, DecodeOptions{MaxSeconds: 0.1}, func([]float64) error { return nil })
	if err == nil {
		t.Fatal("duration over the limit was decoded")
	}
}
