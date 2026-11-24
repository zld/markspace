package audio

import (
	"math"
	"testing"
)

func TestResampleRatio(t *testing.T) {
	r, err := NewResampler(48000, 24000)
	if err != nil {
		t.Fatal(err)
	}
	// 480 mono frames in two blocks: 48k -> 24k must yield ~240 output frames.
	var total int
	for _, blk := range []int{200, 280} {
		out, err := r.Process(Frame{
			Samples: make([]float32, blk), Channels: 1, Rate: 48000,
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.Rate != 24000 {
			t.Fatalf("output rate=%d", out.Rate)
		}
		total += len(out.Samples)
	}
	// Streaming interpolation may be off by one frame per block boundary.
	if math.Abs(float64(total)-240) > 2 {
		t.Fatalf("resampled frames=%d want ~240", total)
	}
}

func TestResampleInterpolation(t *testing.T) {
	r, _ := NewResampler(8000, 16000)
	// Linear ramp 0,1,2,3 -> upsampled values must stay inside the ramp.
	in := Frame{Samples: []float32{0, 1, 2, 3}, Channels: 1, Rate: 8000}
	out, err := r.Process(in)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Samples) < 5 {
		t.Fatalf("upsample produced %d samples", len(out.Samples))
	}
	prev := float32(-1)
	for i, v := range out.Samples {
		if v < prev || v < 0 || v > 3 {
			t.Fatalf("sample %d=%v violates monotone interpolation", i, v)
		}
		prev = v
	}
	if _, err := NewResampler(12345, 48000); err == nil {
		t.Fatal("unsupported rate must be rejected")
	}
}

func TestLayoutUpMixAndDownMix(t *testing.T) {
	mono := Frame{
		Samples:  []float32{0.8, 0.4},
		Channels: 1, Rate: 48000,
	}
	up, err := NewLayoutMapper(1, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	stereo, err := up.Process(mono)
	if err != nil {
		t.Fatal(err)
	}
	// Missing second channel is silence; the first is carried through.
	want := []float32{0.8, 0, 0.4, 0}
	for i := range want {
		if stereo.Samples[i] != want[i] {
			t.Fatalf("up-mix[%d]=%v want %v", i, stereo.Samples[i], want[i])
		}
	}

	surround := Frame{
		// L R C LFE Ls Rs, one frame
		Samples:  []float32{1, 1, 1, 0, 1, 1},
		Channels: 6, Rate: 48000,
	}
	down, err := NewLayoutMapper(6, 2, nil)
	if err != nil {
		t.Fatal(err)
	}
	st, err := down.Process(surround)
	if err != nil {
		t.Fatal(err)
	}
	if st.Channels != 2 || len(st.Samples) != 2 {
		t.Fatalf("down-mix shape wrong: %+v", st)
	}
	if st.Samples[0] <= 1.0 || st.Samples[0] > 2.5 {
		t.Fatalf("folded L out of plausible range: %v", st.Samples[0])
	}

	if _, err := NewLayoutMapper(2, 2, [][]float32{{1}}); err == nil {
		t.Fatal("matrix dimension mismatch must be rejected")
	}
}

func TestLoudnessNormalizeAndClip(t *testing.T) {
	// Scenario 1: a bounded block is normalized onto the -23 LUFS target.
	n := NewNormalizer(-23, 0.95)
	f := Frame{Samples: make([]float32, 2048), Channels: 1, Rate: 48000}
	for i := range f.Samples {
		f.Samples[i] = 0.8
	}
	n.Measure(f)
	out := n.Normalize(f)
	if out.Samples[0] == f.Samples[0] {
		t.Fatal("normalizer must apply a gain different from unity")
	}
	st := n.Stats()
	if math.Abs(st.LoudnessDelta) > 0.5 {
		t.Fatalf("normalized loudness off target by %.2f LUFS", st.LoudnessDelta)
	}
	if st.Clips != 0 {
		t.Fatalf("in-range block must not clip, got %d", st.Clips)
	}

	// Scenario 2: over-limit samples are hard-limited and every clip counted.
	hotN := NewNormalizer(-23, 0.95)
	hot := Frame{Samples: make([]float32, 1024), Channels: 1, Rate: 48000}
	for i := range hot.Samples {
		hot.Samples[i] = 10
	}
	hotN.Measure(hot)
	limited := hotN.Normalize(hot)
	for _, v := range limited.Samples {
		if v > 0.95+1e-6 || v < -0.95-1e-6 {
			t.Fatalf("peak limiter violated: %v", v)
		}
	}
	if hotN.Stats().Clips != int64(len(hot.Samples)) {
		t.Fatalf("every over-limit sample must be counted: %+v", hotN.Stats())
	}

	var empty Meter
	if empty.Stats().IntegratedLUFS != SilenceFloorLUFS {
		t.Fatal("silent stream must report the silence floor")
	}
}
