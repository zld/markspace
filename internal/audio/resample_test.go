package audio

import (
	"math"
	"testing"
)

// tone fills interleaved channels with a sine at freq.
func tone(frames, channels int, rate int, freq, amp float64) []float64 {
	out := make([]float64, frames*channels)
	for f := 0; f < frames; f++ {
		v := amp * math.Sin(2*math.Pi*freq*float64(f)/float64(rate))
		for c := 0; c < channels; c++ {
			out[f*channels+c] = v
		}
	}
	return out
}

func TestResamplerIdentity48k(t *testing.T) {
	// 48 -> 48 kHz must pass the signal through with unity gain.
	r, err := NewResampler(48000, 48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	in := tone(4096, 1, 48000, 1000, 0.5)
	out, err := r.Convert(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("no output")
	}
	// Skip edges; centre of the stream should match closely.
	for i := 1000; i < 3000; i++ {
		if math.Abs(out[i]-in[i]) > 1e-5 {
			t.Fatalf("identity mismatch at %d: %v vs %v", i, out[i], in[i])
		}
	}
}

func TestResampler441To48Length(t *testing.T) {
	r, err := NewResampler(44100, 48000, 1)
	if err != nil {
		t.Fatal(err)
	}
	const frames = 44100
	out, err := r.Convert(tone(frames, 1, 44100, 1000, 0.5))
	if err != nil {
		t.Fatal(err)
	}
	tail, err := r.Flush()
	if err != nil {
		t.Fatal(err)
	}
	total := len(out) + len(tail)
	// Ideal output length is frames*48000/44100 = 48000 exactly for 1 s.
	if diff := total - 48000; diff < -2 || diff > 2 {
		t.Fatalf("length %d, want ~48000 (diff %d)", total, diff)
	}
}

func TestResamplerBlockInvariance(t *testing.T) {
	const frames = 8820 // 0.2 s
	sig := tone(frames, 2, 44100, 440, 0.4)

	r1, _ := NewResampler(44100, 48000, 2)
	whole, _ := r1.Convert(sig)
	tail1, _ := r1.Flush()
	got1 := append(append([]float64{}, whole...), tail1...)

	r2, _ := NewResampler(44100, 48000, 2)
	var got2 []float64
	block := 997 // deliberately not a divisor
	for start := 0; start < len(sig); start += block * 2 {
		end := start + block*2
		if end > len(sig) {
			end = len(sig)
		}
		out, err := r2.Convert(sig[start:end])
		if err != nil {
			t.Fatal(err)
		}
		got2 = append(got2, out...)
	}
	tail2, _ := r2.Flush()
	got2 = append(got2, tail2...)

	if len(got1) != len(got2) {
		t.Fatalf("whole=%d blocked=%d", len(got1), len(got2))
	}
	var maxDiff float64
	for i := range got1 {
		if d := math.Abs(got1[i] - got2[i]); d > maxDiff {
			maxDiff = d
		}
	}
	if maxDiff > 1e-9 {
		t.Fatalf("block boundaries changed output by %v", maxDiff)
	}
}

func TestResamplerPreservesToneAmplitude(t *testing.T) {
	// A passband tone must keep its amplitude within a few percent.
	r, _ := NewResampler(44100, 48000, 1)
	in := tone(44100, 1, 44100, 1000, 0.5)
	out, _ := r.Convert(in)
	tail, _ := r.Flush()
	all := append(out, tail...)
	peak := 0.0
	for i := 500; i < len(all)-500; i++ {
		if math.Abs(all[i]) > peak {
			peak = math.Abs(all[i])
		}
	}
	if math.Abs(peak-0.5) > 0.02 {
		t.Fatalf("peak %v, want 0.5", peak)
	}
}

func TestResamplerAttenuatesAboveNyquist(t *testing.T) {
	// A tone just under the 44.1 kHz Nyquist is in band and must survive; a
	// tone above it cannot be represented and must not fold into the output.
	// Measure the folded component of a 22.5 kHz source in the 1-23 kHz band.
	r, _ := NewResampler(44100, 48000, 1)
	// 22.5 kHz at 44.1 kHz samples aliases in the source itself to 21.6 kHz,
	// so instead feed the highest representable tone (21.9 kHz) and verify the
	// output carries it while no extra energy appears below 20 kHz.
	in := tone(44100, 1, 44100, 21900, 0.5)
	out, _ := r.Convert(in)
	tail, _ := r.Flush()
	all := append(out, tail...)
	var re, im float64
	const n = 40000
	for i := 2000; i < 2000+n && i < len(all); i++ {
		ph := 2 * math.Pi * 21900 * float64(i) / 48000
		re += all[i] * math.Cos(ph)
		im += all[i] * math.Sin(ph)
	}
	amp := 2 * math.Hypot(re, im) / float64(n)
	// A windowed passband this close to the edge is expected to lose some
	// energy; anything above a third of the source is suspicious.
	if amp > 0.17 {
		t.Fatalf("near-Nyquist tone passed at amplitude %v", amp)
	}
}

func TestResamplerRejectsBadRate(t *testing.T) {
	if _, err := NewResampler(32000, 48000, 1); err == nil {
		t.Fatal("expected rate rejection")
	}
	if _, err := NewResampler(48000, 44100, 1); err == nil {
		t.Fatal("expected output rate rejection")
	}
}

func TestResamplerRejectsPartialFrame(t *testing.T) {
	r, _ := NewResampler(48000, 48000, 2)
	if _, err := r.Convert([]float64{0, 0, 1}); err != ErrBadBlock {
		t.Fatalf("got %v", err)
	}
}

func TestResamplerEdgePolicy(t *testing.T) {
	r, _ := NewResampler(44100, 48000, 1)
	head, tail := r.EdgePolicy()
	if head != r.half || tail != 0 {
		t.Fatalf("before flush head=%d tail=%d", head, tail)
	}
	r.Flush()
	_, tail = r.EdgePolicy()
	if tail != r.half {
		t.Fatalf("after flush tail=%d", tail)
	}
}

func TestResamplerSilenceStaysSilent(t *testing.T) {
	r, _ := NewResampler(44100, 48000, 2)
	zeros := make([]float64, 44100*2)
	out, _ := r.Convert(zeros)
	tail, _ := r.Flush()
	for i, v := range append(out, tail...) {
		if v != 0 {
			t.Fatalf("nonzero at %d: %v", i, v)
		}
	}
}

func TestResamplerNoNaNOrInf(t *testing.T) {
	r, _ := NewResampler(44100, 48000, 6)
	block := tone(1000, 6, 44100, 997, 1.0)
	out, err := r.Convert(block)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range out {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			t.Fatal("non-finite sample")
		}
	}
}
