package audio

import (
	"context"
	"math"
	"os/exec"
	"testing"
)

func TestToStereoLayouts(t *testing.T) {
	mono, err := ToStereo([]float32{0.5, -0.25}, 1)
	if err != nil || len(mono) != 4 || mono[0] != mono[1] || mono[2] != mono[3] {
		t.Fatalf("mono duplication broken: %v err=%v", mono, err)
	}
	st, err := ToStereo([]float32{0.1, 0.2}, 2)
	if err != nil || st[0] != 0.1 || st[1] != 0.2 {
		t.Fatal("stereo order must be preserved")
	}
	if _, err := ToStereo(nil, 6); err == nil {
		t.Fatal("6-channel layout must be rejected")
	}
}

func TestResamplerExactRateAndDrift(t *testing.T) {
	// Identity rate must reproduce values exactly (within interpolation).
	r := NewResampler(48000, 1)
	in := make([]float32, 480)
	for i := range in {
		in[i] = float32(i)
	}
	out := r.Process(in)
	if len(out) != 479 { // last frame is carried for the interpolation window
		t.Fatalf("identity chunk produced %d frames, want 479", len(out))
	}
	out = append(out, r.Flush()...)
	if len(out) != 480 || out[100] != 100 {
		t.Fatalf("identity resample broken: len=%d v=%v", len(out), out[100])
	}

	// 44.1k -> 48k over many chunks must stay drift-free.
	rs := NewResampler(44100, 1)
	var total int
	chunk := make([]float32, 1024)
	for i := range chunk {
		chunk[i] = float32(i)
	}
	for c := 0; c < 100; c++ {
		total += len(rs.Process(chunk))
	}
	total += len(rs.Flush())
	wantF := float64(100*1024) * 48000.0 / 44100.0
	want := int(wantF)
	if total < want-2 || total > want+2 {
		t.Fatalf("resampled total %d, want %d±2 (drift)", total, want)
	}
}

func TestResamplerChunkedEqualsOneShot(t *testing.T) {
	src := make([]float32, 4410)
	for i := range src {
		src[i] = float32(math.Sin(2 * math.Pi * float64(i) / 64))
	}
	one := NewResampler(44100, 1)
	a := append(one.Process(src), one.Flush()...)

	two := NewResampler(44100, 1)
	var b []float32
	for i := 0; i < len(src); i += 512 {
		end := i + 512
		if end > len(src) {
			end = len(src)
		}
		b = append(b, two.Process(src[i:end])...)
	}
	b = append(b, two.Flush()...)
	if len(a) != len(b) {
		t.Fatalf("one-shot %d vs chunked %d frames", len(a), len(b))
	}
	for i := range a {
		if math.Abs(float64(a[i]-b[i])) > 1e-5 {
			t.Fatalf("chunk drift at %d: %v vs %v", i, a[i], b[i])
		}
	}
}

func TestLoudnessTargetAndCeiling(t *testing.T) {
	// Full-scale sine: RMS = 1/sqrt(2); normalization should hit -18 dBFS.
	n := 4800
	s := make([]float32, n)
	for i := 0; i < n; i++ {
		s[i] = float32(math.Sin(2 * math.Pi * float64(i) / 48))
	}
	stereo, _ := ToStereo(s, 1)
	out, rep := NormalizeLoudness(stereo)
	if rep.Limited {
		t.Fatal("normal material must not be peak-limited")
	}
	if math.Abs(rep.OutputRMSDB-TargetRMSDBFS) > 0.05 {
		t.Fatalf("output RMS %.3f dBFS, want %.1f±0.05", rep.OutputRMSDB, TargetRMSDBFS)
	}
	if rep.OutputPeakDB > PeakCeilingDB+1e-9 {
		t.Fatal("peak above ceiling")
	}
	if pass, _ := VerifyLoudness(rep, 0.2); !pass {
		t.Fatal("loudness verification failed")
	}
	// Sanity: output is scaled and finite.
	for _, v := range out {
		if math.IsNaN(float64(v)) {
			t.Fatal("NaN in normalized output")
		}
	}
}

func TestLoudnessPeakLimiterEngages(t *testing.T) {
	// A sparse click: very low RMS but peak 1.0 -> RMS gain would clip,
	// so the limiter must cap gain at the safe value and report it.
	s := make([]float32, 48000)
	s[0] = 1.0
	s[1] = 1.0
	out, rep := NormalizeLoudness(s)
	if !rep.Limited {
		t.Fatal("peak limiter should engage for sparse transient")
	}
	if rep.OutputPeakDB > PeakCeilingDB+1e-9 {
		t.Fatalf("limited peak %.3f exceeds ceiling", rep.OutputPeakDB)
	}
	if pass, reason := VerifyLoudness(rep, 0.2); !pass {
		t.Fatalf("limited block should pass with report: %s", reason)
	}
	_ = out
}

func TestFFmpegDecodeAAC(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	// Generate 0.5s of 1 kHz stereo AAC-LC in ADTS via ffmpeg, then decode.
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=0.5",
		"-c:a", "aac", "-b:a", "128k", "-ar", "48000", "-ac", "2",
		"-f", "adts", "pipe:1")
	data, err := gen.Output()
	if err != nil {
		t.Fatalf("generate AAC: %v", err)
	}
	pcm, err := dec.DecodeStream(context.Background(),
		StreamSpec{Codec: "aac-lc", Container: "adts", SrcRate: 48000, Channels: 2}, data)
	if err != nil {
		t.Fatal(err)
	}
	// AAC priming/padding adds roughly one frame (1024) each side.
	if pcm.Rate != 48000 || pcm.Channels != 2 || pcm.Frames() < 23000 || pcm.Frames() > 27000 {
		t.Fatalf("decoded pcm rate=%d ch=%d frames=%d", pcm.Rate, pcm.Channels, pcm.Frames())
	}
}

func TestFFmpegDecodeOpus(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not available")
	}
	dec, err := NewDecoder()
	if err != nil {
		t.Fatal(err)
	}
	gen := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=0.5",
		"-c:a", "libopus", "-ar", "48000", "-ac", "1",
		"-f", "matroska", "pipe:1")
	data, err := gen.Output()
	if err != nil {
		t.Fatalf("generate Opus: %v", err)
	}
	pcm, err := dec.DecodeStream(context.Background(),
		StreamSpec{Codec: "opus", Container: "matroska", SrcRate: 48000, Channels: 1}, data)
	if err != nil {
		t.Fatal(err)
	}
	if pcm.Frames() < 23000 || pcm.Frames() > 25000 {
		t.Fatalf("opus frames=%d", pcm.Frames())
	}
}
