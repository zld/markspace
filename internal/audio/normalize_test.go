package audio

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func writeWAV(t *testing.T, path string, rate uint32, channels, bits uint16, frames int) {
	t.Helper()
	dataSize := uint32(frames * int(channels) * int(bits) / 8)
	buf := make([]byte, 44+dataSize)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], 36+dataSize)
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16)
	binary.LittleEndian.PutUint16(buf[20:22], 1) // PCM
	binary.LittleEndian.PutUint16(buf[22:24], channels)
	binary.LittleEndian.PutUint32(buf[24:28], rate)
	binary.LittleEndian.PutUint32(buf[28:32], rate*uint32(channels)*uint32(bits)/8)
	binary.LittleEndian.PutUint16(buf[32:34], uint16(channels)*bits/8)
	binary.LittleEndian.PutUint16(buf[34:36], bits)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], dataSize)
	// 440 Hz fundamental plus a 660 Hz harmonic; harmonic energy guarantees
	// enough gated blocks for a finite integrated-loudness measurement.
	for i := 0; i < frames; i++ {
		for c := 0; c < int(channels); c++ {
			freq := 440.0
			amp := 0.35
			if c == 1 {
				freq, amp = 660.0, 0.18
			}
			v := 0.8*math.Sin(2*math.Pi*freq*float64(i)/float64(rate)) +
				0.2*math.Sin(2*math.Pi*freq*2*float64(i)/float64(rate))
			sample := int16(amp * float64(math.MaxInt16) * v)
			off := 44 + (i*int(channels)+c)*2
			binary.LittleEndian.PutUint16(buf[off:off+2], uint16(sample))
		}
	}
	if err := os.WriteFile(path, buf, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestProbeWAV(t *testing.T) {
	dir := t.TempDir()
	mono := filepath.Join(dir, "mono.wav")
	writeWAV(t, mono, 24000, 1, 16, 2400)
	info, err := ProbeWAV(mono)
	if err != nil {
		t.Fatal(err)
	}
	if info.Channels != 1 || info.SampleRate != 24000 || info.Bits != 16 {
		t.Fatalf("probe = %+v", info)
	}
	missing := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(missing, []byte("not a wav file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeWAV(missing); !errors.Is(err, ErrMissingTrack) {
		t.Fatalf("non-WAV must fail with ErrMissingTrack, got %v", err)
	}
}

func TestBuildFilterGraph(t *testing.T) {
	if g, err := buildFilterGraph(WAVInfo{Channels: 1}); err != nil || g == "" {
		t.Fatalf("mono graph: %q %v", g, err)
	}
	if g, err := buildFilterGraph(WAVInfo{Channels: 2}); err != nil || g == "" {
		t.Fatalf("stereo graph: %q %v", g, err)
	}
	if _, err := buildFilterGraph(WAVInfo{Channels: 6}); !errors.Is(err, ErrUnsupportedLayout) {
		t.Fatalf("6-channel input must fail explicitly, got %v", err)
	}
}

func TestParseLoudnessJSON(t *testing.T) {
	stderr := "ffmpeg banner noise\n" + `{
		"input_i" : "-20.12",
		"input_tp" : "-3.40",
		"input_lra" : "4.20",
		"input_thresh" : "-30.50",
		"output_i" : "-16.02",
		"output_tp" : "-1.70",
		"output_lra" : "3.10",
		"output_thresh" : "-26.40",
		"normalization_type" : "linear",
		"target_offset" : "0.12"
	}`
	m, err := ParseLoudnessJSON(stderr)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(float64(m.InputI)-(-20.12)) > 1e-9 || m.Normalization != "linear" ||
		math.Abs(float64(m.TargetOffset)-0.12) > 1e-9 {
		t.Fatalf("parsed metrics wrong: %+v", m)
	}
	if _, err := ParseLoudnessJSON("no json here"); !errors.Is(err, ErrNoLoudnessReport) {
		t.Fatalf("missing report must fail, got %v", err)
	}
}

func TestFilterArguments(t *testing.T) {
	p := DefaultLoudness()
	g := p.passOneFilter()
	if g != "loudnorm=I=-16:TP=-1.5:LRA=11:print_format=json" {
		t.Fatalf("pass1 filter = %q", g)
	}
}

func TestNormalize_EndToEnd(t *testing.T) {
	n, err := NewNormalizer()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src_mono_24k.wav")
	dst := filepath.Join(dir, "out_stereo_48k.wav")
	writeWAV(t, src, 24000, 1, 16, 9600) // 4 seconds mono 24 kHz

	res, err := n.Normalize(context.Background(), src, dst)
	if err != nil {
		t.Fatal(err)
	}
	out, err := ProbeWAV(dst)
	if err != nil {
		t.Fatal(err)
	}
	if out.SampleRate != TargetRate || out.Channels != TargetChannels || out.Bits != 16 {
		t.Fatalf("output spec wrong: %+v", out)
	}
	if math.Abs(float64(res.OutputMetrics.OutputI)-(-16)) > 1.0 {
		t.Fatalf("integrated loudness off target: %+v", res.OutputMetrics)
	}
	if res.FFmpegVersion == "" || res.FFmpegVersion == "unknown" {
		t.Fatal("ffmpeg version must be captured for the cache key")
	}
	if info, err := os.Stat(dst); err != nil || info.Size() == 0 {
		t.Fatal("output artifact missing or empty")
	}
}

func TestNormalize_RejectsUnsupportedBeforeFfmpeg(t *testing.T) {
	n, err := NewNormalizer()
	if err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "surround.wav")
	writeWAV(t, src, 48000, 6, 16, 480)
	dst := filepath.Join(dir, "out.wav")
	_, err = n.Normalize(context.Background(), src, dst)
	if !errors.Is(err, ErrUnsupportedLayout) {
		t.Fatalf("want ErrUnsupportedLayout, got %v", err)
	}
}
